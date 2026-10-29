// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package callcrypto distributes and consumes Element Call (MatrixRTC) SFrame media keys for the
// JMPChat bridge, so calls can run in the same encrypted rooms as the chats.
//
// One Manager manages one call device per ghost user (the bridge's appservice user). The device
// authors the MatrixRTC membership with the same device ID and owns the Olm machine that
// de-/encrypts the to-device key events. Each room then gets a RoomSession which implements the
// calls core's FrameCrypto seam: it seals outgoing audio with the ghost's own key and opens
// incoming audio with the sender's key.
package callcrypto

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/williamkray/mrtc-sip-bridge/pkg/matrixrtc"
)

// CallDeviceID is the stable Matrix device ID of every ghost's call device. A stable ID lets other
// room members keep sending the ghost call keys and lets the membership, the Olm account and the
// SFrame keys survive bridge restarts.
const CallDeviceID = id.DeviceID("SIPCALL")

// CallDeviceName is the display name of the call device.
const CallDeviceName = "JMPChat calls"

// Client is the subset of *mautrix.Client the call crypto code uses: the device-scoped client, so
// to-device messages are sent as the call device.
type Client interface {
	SendToDevice(ctx context.Context, eventType event.Type, req *mautrix.ReqSendToDevice) (*mautrix.RespSendToDevice, error)
	State(ctx context.Context, roomID id.RoomID) (mautrix.RoomStateMap, error)
}

// Machine is the subset of *crypto.OlmMachine the call crypto code uses.
type Machine interface {
	HandleEncryptedEvent(ctx context.Context, evt *event.Event) *crypto.DecryptedOlmEvent
	EncryptToDevices(ctx context.Context, eventType event.Type, req *mautrix.ReqSendToDevice) (*mautrix.ReqSendToDevice, error)
}

// Device is one ghost's call device.
type Device struct {
	Client  Client
	Machine Machine
}

// DeviceProvider prepares (creating on first use, loading afterwards) the call device of a ghost.
type DeviceProvider func(ctx context.Context, userID id.UserID) (Device, error)

// ManagerOptions configures a Manager.
type ManagerOptions struct {
	Log zerolog.Logger
	// Device is required. It must create the call device on first use and return the loaded
	// device afterwards.
	Device DeviceProvider
	// Now defaults to time.Now.
	Now func() time.Time
}

// Manager owns the call devices and the per-room key sessions.
type Manager struct {
	log       zerolog.Logger
	newDevice DeviceProvider
	now       func() time.Time

	devMu   sync.Mutex
	devices map[id.UserID]Device

	mu       sync.Mutex
	sessions map[id.RoomID]*RoomSession
}

// NewManager builds a Manager.
func NewManager(opts ManagerOptions) *Manager {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Manager{
		log:       opts.Log,
		newDevice: opts.Device,
		now:       now,
		devices:   make(map[id.UserID]Device),
		sessions:  make(map[id.RoomID]*RoomSession),
	}
}

// Session returns the key session of a room, or nil when the room has none.
func (m *Manager) Session(roomID id.RoomID) *RoomSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[roomID]
}

// DropSession forgets the key session of a room; a later call starts with a fresh key.
func (m *Manager) DropSession(roomID id.RoomID) {
	m.mu.Lock()
	delete(m.sessions, roomID)
	m.mu.Unlock()
}

// EnsureSession returns the key session of a room, creating it (and the ghost's call device and its
// first key) on first use. It seeds the participant list from the room state and starts
// distributing the ghost's key; both are best effort.
func (m *Manager) EnsureSession(ctx context.Context, roomID id.RoomID, ghost id.UserID) (*RoomSession, error) {
	if sess := m.Session(roomID); sess != nil {
		return sess, nil
	}
	dev, err := m.ensureDevice(ctx, ghost)
	if err != nil {
		return nil, err
	}
	sess := newRoomSession(m.log.With().Str("room_id", roomID.String()).Logger(), m.now, roomID, ghost, dev)
	if err := sess.ensureOwnKey(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	if existing := m.sessions[roomID]; existing != nil {
		m.mu.Unlock()
		return existing, nil
	}
	m.sessions[roomID] = sess
	m.mu.Unlock()
	m.log.Info().Str("room_id", roomID.String()).Str("ghost", ghost.String()).Msg("callcrypto: call key session ready")

	if err := sess.refreshTargets(ctx); err != nil {
		sess.log.Warn().Err(err).Msg("callcrypto: could not read the room state for key distribution")
	}
	sess.maybeShare(m.now())
	return sess, nil
}

// HandleToDevice decrypts and ingests one to-device encryption-key event. It reports whether the
// event was addressed to a call device (events for other devices or users return false).
func (m *Manager) HandleToDevice(ctx context.Context, evt *event.Event) bool {
	if evt == nil || evt.Type.Type != event.ToDeviceEncrypted.Type || evt.Type.Class != event.ToDeviceEventType {
		return false
	}
	if id.DeviceID(evt.ToDeviceID) != CallDeviceID || evt.ToUserID == "" {
		return false
	}
	dev, ok := m.deviceFor(evt.ToUserID)
	if !ok {
		m.log.Debug().Str("user_id", evt.ToUserID.String()).
			Msg("callcrypto: key event for a user without a call device")
		return true
	}
	de := dev.Machine.HandleEncryptedEvent(ctx, evt)
	if de == nil {
		m.log.Debug().Str("user_id", evt.ToUserID.String()).Msg("callcrypto: could not decrypt a to-device key event")
		return true
	}
	if de.Type.Type != KeyEventType.Type {
		return true
	}
	raw := de.Content.VeryRaw
	if len(raw) == 0 {
		raw, _ = json.Marshal(de.Content.Raw)
	}
	senderDevice := id.DeviceID("")
	if de.SenderDevice != nil {
		senderDevice = de.SenderDevice.DeviceID
	}
	kev, secret, err := parseKeyEvent(raw, senderDevice)
	if err != nil {
		m.log.Warn().Err(err).Str("sender", de.Sender.String()).Msg("callcrypto: rejected a media key event")
		return true
	}
	sess := m.Session(kev.RoomID)
	if sess == nil {
		m.log.Debug().Str("room_id", kev.RoomID.String()).Msg("callcrypto: media key event for a room without a call session")
		return true
	}
	if err := sess.ingestKey(de.Sender, senderDevice, kev.entry().Index, secret); err != nil {
		m.log.Warn().Err(err).Str("sender", de.Sender.String()).Msg("callcrypto: could not store a media key")
		return true
	}
	m.log.Info().
		Str("room_id", kev.RoomID.String()).
		Str("sender", de.Sender.String()).
		Str("sender_device", senderDevice.String()).
		Uint32("key_index", kev.entry().Index).
		Msg("callcrypto: media key received")
	// The sender is in the call in this room, so make sure they have our current key as well.
	if sess.addTarget(Target{User: de.Sender, Device: senderDevice}) {
		sess.maybeShare(m.now())
	}
	return true
}

// HandleMemberships feeds MatrixRTC membership updates of a room into its key session, so keys go
// to new participants and a rotation covers the ones who left. Rooms without a session (no call
// has started in the bridge yet) are ignored.
func (m *Manager) HandleMemberships(ctx context.Context, roomID id.RoomID, members []matrixrtc.Membership) {
	sess := m.Session(roomID)
	if sess == nil || len(members) == 0 {
		return
	}
	sess.applyMemberships(ctx, members)
}

// ensureDevice returns the call device of user, creating it on first use.
func (m *Manager) ensureDevice(ctx context.Context, userID id.UserID) (Device, error) {
	m.devMu.Lock()
	defer m.devMu.Unlock()
	if dev, ok := m.devices[userID]; ok {
		return dev, nil
	}
	dev, err := m.newDevice(ctx, userID)
	if err != nil {
		return Device{}, err
	}
	m.devices[userID] = dev
	return dev, nil
}

func (m *Manager) deviceFor(userID id.UserID) (Device, bool) {
	m.devMu.Lock()
	defer m.devMu.Unlock()
	dev, ok := m.devices[userID]
	return dev, ok
}
