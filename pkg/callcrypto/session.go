// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package callcrypto

import (
	"context"
	"encoding/base64"
	"errors"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/williamkray/mrtc-sip-bridge/pkg/core"
	"github.com/williamkray/mrtc-sip-bridge/pkg/matrixrtc"

	"jmpchat/pkg/sframe"
)

const (
	// shareRetryInterval throttles background key redistribution attempts.
	shareRetryInterval = 5 * time.Second
	// shareAttempts is how often the background loop retries one distribution round.
	shareAttempts = 20
	// shareTimeout bounds one distribution round (device fetching + Olm encryption + send).
	shareTimeout = 30 * time.Second
)

var (
	errNoMemberKey = errors.New("callcrypto: no media key for this participant yet")
	errNoDevice    = errors.New("callcrypto: media key event without a sender device")
)

// Target is one member device a media key is distributed to.
type Target struct {
	User   id.UserID
	Device id.DeviceID
}

// RoomSession holds the SFrame keys of one room: the ghost's own key ring and one key table per
// member device, indexed by every LiveKit identity that device can appear as.
type RoomSession struct {
	log zerolog.Logger
	now func() time.Time

	roomID id.RoomID
	ghost  id.UserID
	dev    Device

	own *sframe.KeyTable

	mu        sync.Mutex
	members   map[string]*sframe.KeyTable
	targets   map[Target]struct{}
	shared    map[Target]uint8
	sharing   bool
	lastShare time.Time
}

var _ core.FrameCrypto = (*RoomSession)(nil)

func newRoomSession(log zerolog.Logger, now func() time.Time, roomID id.RoomID, ghost id.UserID, dev Device) *RoomSession {
	return &RoomSession{
		log:     log,
		now:     now,
		roomID:  roomID,
		ghost:   ghost,
		dev:     dev,
		own:     sframe.NewKeyTable(),
		members: make(map[string]*sframe.KeyTable),
		targets: make(map[Target]struct{}),
		shared:  make(map[Target]uint8),
	}
}

// RoomID returns the room the session belongs to.
func (s *RoomSession) RoomID() id.RoomID { return s.roomID }

// ensureOwnKey creates the ghost's first key (index 0) and selects it for outbound encryption.
func (s *RoomSession) ensureOwnKey() error {
	if s.own.Len() > 0 {
		return nil
	}
	secret, err := sframe.NewSecret()
	if err != nil {
		return err
	}
	if err := s.own.Insert(0, secret, s.now()); err != nil {
		return err
	}
	return s.own.Select(0)
}

// Sealer implements core.FrameCrypto: it encrypts every published audio frame with the ghost's
// current key. A pending rotation is promoted here once its use delay has elapsed.
func (s *RoomSession) Sealer() func([]byte) ([]byte, error) {
	return func(frame []byte) ([]byte, error) {
		now := s.now()
		if kid, ok := s.own.PromotePending(now); ok {
			s.log.Info().Uint8("key_index", kid).Msg("callcrypto: the rotated media key is now in use")
		}
		s.maybeShare(now)
		if len(frame) == 0 {
			return frame, nil
		}
		return s.own.Seal(frame)
	}
}

// Opener implements core.FrameCrypto: it decrypts frames from one LiveKit identity. Frames whose
// key has not arrived yet return an error and are dropped (and counted) by the calls core.
func (s *RoomSession) Opener(identity string) func([]byte) ([]byte, error) {
	s.mu.Lock()
	table := s.members[identity]
	s.mu.Unlock()
	if table == nil {
		return func([]byte) ([]byte, error) { return nil, errNoMemberKey }
	}
	return func(frame []byte) ([]byte, error) { return table.Open(frame, nil) }
}

// ingestKey stores a member's media key under every identity that member device can appear as.
func (s *RoomSession) ingestKey(userID id.UserID, deviceID id.DeviceID, index uint32, secret []byte) error {
	if deviceID == "" {
		return errNoDevice
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, identity := range MemberIdentity(userID, deviceID) {
		table := s.members[identity]
		if table == nil {
			table = sframe.NewKeyTable()
			s.members[identity] = table
		}
		if err := table.Insert(uint8(index), secret, s.now()); err != nil {
			return err
		}
	}
	return nil
}

// addTarget records a member device our key must reach. It reports whether the target is new.
func (s *RoomSession) addTarget(t Target) bool {
	if t.User == "" || t.Device == "" {
		return false
	}
	if t.User == s.ghost && t.Device == CallDeviceID {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.targets[t]; ok {
		return false
	}
	s.targets[t] = struct{}{}
	delete(s.shared, t)
	return true
}

// Targets returns the member devices keys are distributed to.
func (s *RoomSession) Targets() []Target {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Target, 0, len(s.targets))
	for t := range s.targets {
		out = append(out, t)
	}
	return out
}

// applyMemberships updates the participant set from MatrixRTC membership events: new devices get
// the current key, and a departure rotates it so the leaver cannot read later media.
func (s *RoomSession) applyMemberships(ctx context.Context, members []matrixrtc.Membership) {
	changed, removed := false, false
	for _, mem := range members {
		if mem.UserID == "" || mem.DeviceID == "" {
			continue
		}
		if mem.UserID == s.ghost && id.DeviceID(mem.DeviceID) == CallDeviceID {
			continue
		}
		t := Target{User: mem.UserID, Device: id.DeviceID(mem.DeviceID)}
		if mem.Live {
			if s.addTarget(t) {
				changed = true
			}
			continue
		}
		s.mu.Lock()
		if _, ok := s.targets[t]; ok {
			delete(s.targets, t)
			delete(s.shared, t)
			changed, removed = true, true
		}
		s.mu.Unlock()
	}
	if removed {
		if err := s.rotate(); err != nil {
			s.log.Warn().Err(err).Msg("callcrypto: could not rotate the media key after a departure")
		} else {
			s.log.Info().Msg("callcrypto: rotating the outbound media key after a departure")
		}
	}
	if changed {
		s.maybeShare(s.now())
	}
}

// refreshTargets seeds the participant set from the room state: the membership events of the
// current call may predate the bridge's participation in the room.
func (s *RoomSession) refreshTargets(ctx context.Context) error {
	state, err := s.dev.Client.State(ctx, s.roomID)
	if err != nil {
		return err
	}
	now := s.now()
	added := false
	for typ, byKey := range state {
		if typ.Type != matrixrtc.CallMember.Type && typ.Type != matrixrtc.CallMemberAlias.Type {
			continue
		}
		for _, evt := range byKey {
			parsed, ok := matrixrtc.FromMatrixEvent(evt)
			if !ok {
				continue
			}
			mem, ok := matrixrtc.ParseMembership(parsed, now)
			if !ok || !mem.Live {
				continue
			}
			if s.addTarget(Target{User: mem.UserID, Device: id.DeviceID(mem.DeviceID)}) {
				added = true
			}
		}
	}
	if added {
		s.log.Info().Int("devices", len(s.Targets())).Msg("callcrypto: seeded the participant list from the room state")
	}
	return nil
}

// rotate creates the next outbound key. It stays pending (and the old key keeps encrypting) until
// its use delay has elapsed, exactly like Element Call distributes before switching.
func (s *RoomSession) rotate() error {
	secret, err := sframe.NewSecret()
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.own.Rotate(secret, s.now()); err != nil {
		return err
	}
	s.shared = make(map[Target]uint8)
	return nil
}

// newestOwnKey returns the key new participants must receive: a pending rotation when one exists,
// the current key otherwise.
func (s *RoomSession) newestOwnKey() (sframe.Key, bool) {
	if key, ok := s.own.Pending(); ok {
		return key, true
	}
	return s.own.Current()
}

// hasUnsharedLocked reports whether any target is missing the newest outbound key.
func (s *RoomSession) hasUnsharedLocked() bool {
	key, ok := s.newestOwnKey()
	if !ok {
		return false
	}
	for t := range s.targets {
		if s.shared[t] != key.KID {
			return true
		}
	}
	return false
}

// maybeShare starts a background distribution round when targets are missing the newest key. It is
// called from the media path and from membership/key handling, so no other goroutine is needed.
func (s *RoomSession) maybeShare(now time.Time) {
	s.mu.Lock()
	start := !s.sharing && s.hasUnsharedLocked() && now.Sub(s.lastShare) >= shareRetryInterval
	if start {
		s.sharing = true
		s.lastShare = now
	}
	s.mu.Unlock()
	if start {
		go s.shareLoop()
	}
}

func (s *RoomSession) shareLoop() {
	defer func() {
		s.mu.Lock()
		s.sharing = false
		s.mu.Unlock()
	}()
	for attempt := 0; attempt < shareAttempts; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), shareTimeout)
		err := s.ShareOwnKey(ctx)
		cancel()
		if err != nil {
			s.log.Warn().Err(err).Msg("callcrypto: could not distribute the media key")
			time.Sleep(shareRetryInterval)
			continue
		}
		s.mu.Lock()
		done := !s.hasUnsharedLocked()
		s.mu.Unlock()
		if done {
			return
		}
		time.Sleep(time.Second)
	}
	s.log.Warn().Msg("callcrypto: gave up distributing the media key")
}

// ShareOwnKey distributes the newest outbound key to every target device that does not have it.
func (s *RoomSession) ShareOwnKey(ctx context.Context) error {
	key, ok := s.newestOwnKey()
	if !ok {
		return sframe.ErrNoCurrentKey
	}
	s.mu.Lock()
	var need []Target
	for t := range s.targets {
		if s.shared[t] != key.KID {
			need = append(need, t)
		}
	}
	s.mu.Unlock()
	if len(need) == 0 {
		return nil
	}

	content := s.ownKeyContent(key)
	messages := make(map[id.UserID]map[id.DeviceID]*event.Content, len(need))
	for _, t := range need {
		if messages[t.User] == nil {
			messages[t.User] = make(map[id.DeviceID]*event.Content)
		}
		messages[t.User][t.Device] = content
	}
	encrypted, err := s.dev.Machine.EncryptToDevices(ctx, KeyEventType, &mautrix.ReqSendToDevice{Messages: messages})
	if err != nil {
		return err
	}
	if _, err := s.dev.Client.SendToDevice(ctx, event.ToDeviceEncrypted, encrypted); err != nil {
		return err
	}
	s.mu.Lock()
	for _, t := range need {
		s.shared[t] = key.KID
	}
	s.mu.Unlock()
	s.log.Info().Int("devices", len(need)).Uint8("key_index", key.KID).Msg("callcrypto: distributed the media key")
	return nil
}

// ownKeyContent builds the Element Call key event payload for one of the ghost's own keys.
func (s *RoomSession) ownKeyContent(key sframe.Key) *event.Content {
	memberID := LegacyIdentity(s.ghost, CallDeviceID)
	entry := map[string]any{
		"index": uint32(key.KID),
		"key":   base64.RawStdEncoding.EncodeToString(key.Secret),
	}
	return &event.Content{Parsed: map[string]any{
		"room_id":   s.roomID.String(),
		"member_id": memberID,
		"media_key": entry,
		"keys":      entry,
		"member": map[string]any{
			"claimed_device_id": CallDeviceID.String(),
			"id":                memberID,
		},
	}}
}
