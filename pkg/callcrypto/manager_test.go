// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package callcrypto

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/williamkray/mrtc-sip-bridge/pkg/matrixrtc"

	"jmpchat/pkg/sframe"
)

// decryptingMachine is a fakeMachine whose HandleEncryptedEvent returns a fixed decrypted event.
type decryptingMachine struct {
	fakeMachine
	de *crypto.DecryptedOlmEvent
}

func (d *decryptingMachine) HandleEncryptedEvent(_ context.Context, _ *event.Event) *crypto.DecryptedOlmEvent {
	return d.de
}

type managerFixture struct {
	ctx     context.Context
	current time.Time
	client  *fakeClient
	machine Machine
	mgr     *Manager
}

func newManagerFixture(t *testing.T, machine Machine) *managerFixture {
	t.Helper()
	f := &managerFixture{
		ctx:     context.Background(),
		current: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC),
		client:  &fakeClient{},
		machine: machine,
	}
	dev := Device{Client: f.client, Machine: f.machine}
	f.mgr = NewManager(ManagerOptions{
		Log:    zerolog.Nop(),
		Now:    func() time.Time { return f.current },
		Device: func(context.Context, id.UserID) (Device, error) { return dev, nil },
	})
	return f
}

func TestManagerEnsureSessionCachesTheRoomSession(t *testing.T) {
	f := newManagerFixture(t, &fakeMachine{})
	first, err := f.mgr.EnsureSession(f.ctx, testRoomID, testGhost)
	if err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}
	second, err := f.mgr.EnsureSession(f.ctx, testRoomID, testGhost)
	if err != nil {
		t.Fatalf("second EnsureSession: %v", err)
	}
	if first != second {
		t.Fatal("EnsureSession did not return the cached session")
	}
	if f.mgr.Session(testRoomID) != first {
		t.Fatal("Session does not return the created session")
	}
	f.mgr.DropSession(testRoomID)
	if f.mgr.Session(testRoomID) != nil {
		t.Fatal("DropSession did not forget the session")
	}
}

func TestManagerEnsureSessionDeviceError(t *testing.T) {
	mgr := NewManager(ManagerOptions{
		Log:    zerolog.Nop(),
		Device: func(context.Context, id.UserID) (Device, error) { return Device{}, errors.New("boom") },
	})
	if _, err := mgr.EnsureSession(context.Background(), testRoomID, testGhost); err == nil {
		t.Fatal("EnsureSession must fail when the device cannot be created")
	}
}

func TestManagerHandleMembershipsAddsTargets(t *testing.T) {
	f := newManagerFixture(t, &fakeMachine{})
	sess, err := f.mgr.EnsureSession(f.ctx, testRoomID, testGhost)
	if err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}
	f.mgr.HandleMemberships(f.ctx, testRoomID, []matrixrtc.Membership{{
		UserID: testMember, DeviceID: string(testDevice), Live: true,
	}})
	if _, ok := sess.targets[Target{User: testMember, Device: testDevice}]; !ok {
		t.Fatal("a live membership was not added as a key target")
	}
	// Rooms without a call session are ignored instead of panicking.
	f.mgr.HandleMemberships(f.ctx, "!other:example.com", []matrixrtc.Membership{{
		UserID: testMember, DeviceID: string(testDevice), Live: true,
	}})
}

func TestManagerHandleToDeviceIngestsKeys(t *testing.T) {
	machine := &decryptingMachine{}
	f := newManagerFixture(t, machine)
	sess, err := f.mgr.EnsureSession(f.ctx, testRoomID, testGhost)
	if err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}

	secret, err := sframe.NewSecret()
	if err != nil {
		t.Fatalf("new secret: %v", err)
	}
	memberID := LegacyIdentity(testMember, testDevice)
	raw, err := json.Marshal(map[string]any{
		"room_id":   string(testRoomID),
		"member_id": memberID,
		"media_key": map[string]any{"index": 0, "key": base64.RawStdEncoding.EncodeToString(secret)},
		"member":    map[string]any{"claimed_device_id": string(testDevice), "id": memberID},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	machine.de = &crypto.DecryptedOlmEvent{
		Sender:       testMember,
		SenderDevice: &id.Device{DeviceID: testDevice},
		Type:         KeyEventType,
		Content:      event.Content{VeryRaw: raw},
	}

	evt := &event.Event{
		Type:       event.ToDeviceEncrypted,
		Sender:     testMember,
		RoomID:     testRoomID,
		ToDeviceID: CallDeviceID,
		ToUserID:   testGhost,
	}
	if !f.mgr.HandleToDevice(f.ctx, evt) {
		t.Fatal("a key event for the call device was not consumed")
	}

	// The received key decrypts the sender's frames under every identity form.
	aesKey, err := sframe.DeriveKey(secret)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	frame, err := sframe.Seal([]byte("from alice"), aesKey, 0)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	for _, identity := range MemberIdentity(testMember, testDevice) {
		plain, err := sess.Opener(identity)(frame)
		if err != nil {
			t.Fatalf("opener(%q): %v", identity, err)
		}
		if string(plain) != "from alice" {
			t.Fatalf("opener(%q) = %q", identity, plain)
		}
	}
	// The sender is now a target for the ghost's own key distribution too.
	if _, ok := sess.targets[Target{User: testMember, Device: testDevice}]; !ok {
		t.Fatal("the key sender was not added as a target")
	}
}

func TestManagerHandleToDeviceRejections(t *testing.T) {
	f := newManagerFixture(t, &decryptingMachine{})
	if _, err := f.mgr.EnsureSession(f.ctx, testRoomID, testGhost); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}

	evt := func(mods ...func(*event.Event)) *event.Event {
		e := &event.Event{
			Type:       event.ToDeviceEncrypted,
			Sender:     testMember,
			RoomID:     testRoomID,
			ToDeviceID: CallDeviceID,
			ToUserID:   testGhost,
		}
		for _, mod := range mods {
			mod(e)
		}
		return e
	}

	rejected := []struct {
		name string
		evt  *event.Event
	}{
		{"nil event", nil},
		{"plain room event", evt(func(e *event.Event) { e.Type = event.EventEncrypted })},
		{"another device", evt(func(e *event.Event) { e.ToDeviceID = "OTHER" })},
		{"no target user", evt(func(e *event.Event) { e.ToUserID = "" })},
	}
	for _, tc := range rejected {
		if f.mgr.HandleToDevice(f.ctx, tc.evt) {
			t.Errorf("%s: event was consumed", tc.name)
		}
	}

	consumed := []struct {
		name string
		evt  *event.Event
	}{
		{"user without a call device", evt(func(e *event.Event) { e.ToUserID = "@other:example.com" })},
		{"undecryptable", evt()},
	}
	for _, tc := range consumed {
		if !f.mgr.HandleToDevice(f.ctx, tc.evt) {
			t.Errorf("%s: event was not consumed", tc.name)
		}
	}
}
