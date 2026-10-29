// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package callcrypto

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/williamkray/mrtc-sip-bridge/pkg/matrixrtc"

	"jmpchat/pkg/sframe"
)

const (
	testRoomID id.RoomID   = "!room:example.com"
	testGhost  id.UserID   = "@+15550100203:cheogram.com"
	testMember id.UserID   = "@alice:example.com"
	testDevice id.DeviceID = "DEVICE1"
)

type sentToDevice struct {
	eventType event.Type
	req       *mautrix.ReqSendToDevice
}

// fakeClient records to-device sends and serves a fixed room state.
type fakeClient struct {
	sent  []sentToDevice
	state mautrix.RoomStateMap
}

func (f *fakeClient) SendToDevice(_ context.Context, eventType event.Type, req *mautrix.ReqSendToDevice) (*mautrix.RespSendToDevice, error) {
	f.sent = append(f.sent, sentToDevice{eventType: eventType, req: req})
	return &mautrix.RespSendToDevice{}, nil
}

func (f *fakeClient) State(_ context.Context, _ id.RoomID) (mautrix.RoomStateMap, error) {
	if f.state == nil {
		return mautrix.RoomStateMap{}, nil
	}
	return f.state, nil
}

// fakeMachine stands in for the Olm machine: it records what it was asked to encrypt and returns a
// stand-in encrypted request.
type fakeMachine struct {
	eventTypes []event.Type
	reqs       []*mautrix.ReqSendToDevice
}

func (f *fakeMachine) HandleEncryptedEvent(_ context.Context, _ *event.Event) *crypto.DecryptedOlmEvent {
	return nil
}

func (f *fakeMachine) EncryptToDevices(_ context.Context, eventType event.Type, req *mautrix.ReqSendToDevice) (*mautrix.ReqSendToDevice, error) {
	f.eventTypes = append(f.eventTypes, eventType)
	f.reqs = append(f.reqs, req)
	out := &mautrix.ReqSendToDevice{Messages: make(map[id.UserID]map[id.DeviceID]*event.Content, len(req.Messages))}
	for userID, devices := range req.Messages {
		out.Messages[userID] = make(map[id.DeviceID]*event.Content, len(devices))
		for deviceID := range devices {
			out.Messages[userID][deviceID] = &event.Content{Parsed: map[string]any{"ciphertext": "stub"}}
		}
	}
	return out, nil
}

type sessionFixture struct {
	ctx     context.Context
	current time.Time
	client  *fakeClient
	machine *fakeMachine
	session *RoomSession
}

func (f *sessionFixture) advance(d time.Duration) { f.current = f.current.Add(d) }

func newSessionFixture(t *testing.T) *sessionFixture {
	t.Helper()
	f := &sessionFixture{
		ctx:     context.Background(),
		current: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC),
		client:  &fakeClient{},
		machine: &fakeMachine{},
	}
	dev := Device{Client: f.client, Machine: f.machine}
	mgr := NewManager(ManagerOptions{
		Log:    zerolog.Nop(),
		Now:    func() time.Time { return f.current },
		Device: func(context.Context, id.UserID) (Device, error) { return dev, nil },
	})
	sess, err := mgr.EnsureSession(f.ctx, testRoomID, testGhost)
	if err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}
	f.session = sess
	return f
}

func TestSealerUsesTheOwnKey(t *testing.T) {
	f := newSessionFixture(t)
	frame, err := f.session.Sealer()([]byte("hello"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	key, ok := f.session.own.Lookup(0)
	if !ok {
		t.Fatal("own key 0 is missing")
	}
	plain, kid, err := sframe.Open(frame, key.AESKey)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if kid != 0 {
		t.Fatalf("kid = %d, want 0", kid)
	}
	if string(plain) != "hello" {
		t.Fatalf("plain = %q, want %q", plain, "hello")
	}
	if frame2, err := f.session.Sealer()([]byte{}); err != nil || len(frame2) != 0 {
		t.Fatalf("empty frame = %v, %v", frame2, err)
	}
}

func TestOpenerDecryptsMemberFrames(t *testing.T) {
	f := newSessionFixture(t)
	secret, err := sframe.NewSecret()
	if err != nil {
		t.Fatalf("new secret: %v", err)
	}
	if err := f.session.ingestKey(testMember, testDevice, 0, secret); err != nil {
		t.Fatalf("ingestKey: %v", err)
	}
	aesKey, err := sframe.DeriveKey(secret)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	frame, err := sframe.Seal([]byte("from alice"), aesKey, 0)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	for _, identity := range MemberIdentity(testMember, testDevice) {
		plain, err := f.session.Opener(identity)(frame)
		if err != nil {
			t.Fatalf("opener(%q): %v", identity, err)
		}
		if string(plain) != "from alice" {
			t.Fatalf("opener(%q) = %q", identity, plain)
		}
	}
	if _, err := f.session.Opener("unknown-identity")(frame); err == nil {
		t.Fatal("a frame from an unknown identity must not decrypt")
	}
}

func TestShareOwnKeyDistributesToTargets(t *testing.T) {
	f := newSessionFixture(t)
	target := Target{User: testMember, Device: testDevice}
	if !f.session.addTarget(target) {
		t.Fatal("addTarget returned false for a new target")
	}
	if err := f.session.ShareOwnKey(f.ctx); err != nil {
		t.Fatalf("ShareOwnKey: %v", err)
	}
	if len(f.machine.eventTypes) != 1 || f.machine.eventTypes[0] != KeyEventType {
		t.Fatalf("encrypt event types = %v", f.machine.eventTypes)
	}
	if len(f.client.sent) != 1 || f.client.sent[0].eventType != event.ToDeviceEncrypted {
		t.Fatalf("to-device sends = %+v", f.client.sent)
	}
	content := f.machine.reqs[0].Messages[testMember][testDevice]
	if content == nil {
		t.Fatal("no content for the target device")
	}
	parsed, ok := content.Parsed.(map[string]any)
	if !ok {
		t.Fatalf("content = %#v", content.Parsed)
	}
	if parsed["room_id"] != string(testRoomID) {
		t.Fatalf("room_id = %v", parsed["room_id"])
	}
	member, ok := parsed["member"].(map[string]any)
	if !ok || member["claimed_device_id"] != string(CallDeviceID) {
		t.Fatalf("member = %#v", parsed["member"])
	}
	if mediaKey, ok := parsed["media_key"].(map[string]any); !ok || mediaKey["index"] != uint32(0) {
		t.Fatalf("media_key = %#v", parsed["media_key"])
	}
	if f.session.shared[target] != 1 {
		t.Fatal("the shared key index was not recorded")
	}
	if err := f.session.ShareOwnKey(f.ctx); err != nil {
		t.Fatalf("second ShareOwnKey: %v", err)
	}
	if len(f.client.sent) != 1 {
		t.Fatal("the same key was distributed twice")
	}
}

func TestDepartureRotatesTheKey(t *testing.T) {
	f := newSessionFixture(t)
	target := Target{User: testMember, Device: testDevice}
	f.session.addTarget(target)

	// A membership seen as dead removes the target and rotates the outbound key.
	f.session.applyMemberships(f.ctx, []matrixrtc.Membership{{
		UserID: testMember, DeviceID: string(testDevice), Live: false,
	}})
	if _, ok := f.session.targets[target]; ok {
		t.Fatal("a dead membership kept the target")
	}
	pending, ok := f.session.own.Pending()
	if !ok || pending.KID != 1 {
		t.Fatalf("pending key = %+v, %v, want kid 1", pending, ok)
	}

	// The rotated key is not used before its use delay elapsed.
	frame, err := f.session.Sealer()([]byte("before"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, kid, err := sframe.Open(frame, mustAESKey(t, f.session, 0)); err != nil || kid != 0 {
		t.Fatalf("pre-promotion kid = %d, %v", kid, err)
	}

	f.advance(2 * time.Second)
	frame, err = f.session.Sealer()([]byte("after"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	plain, kid, err := sframe.Open(frame, mustAESKey(t, f.session, 1))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if kid != 1 {
		t.Fatalf("post-promotion kid = %d, want 1", kid)
	}
	if string(plain) != "after" {
		t.Fatalf("plain = %q", plain)
	}
}

func mustAESKey(t *testing.T, s *RoomSession, kid uint8) []byte {
	t.Helper()
	key, ok := s.own.Lookup(kid)
	if !ok {
		t.Fatalf("own key %d is missing", kid)
	}
	return key.AESKey
}
