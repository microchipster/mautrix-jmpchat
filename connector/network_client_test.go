// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"fmt"
	"testing"
	"time"

	"gosrc.io/xmpp/stanza"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func TestMyNetworkClientTryStartConnect(t *testing.T) {
	client := &MyNetworkClient{}

	attempt, _, started := client.tryStartConnect()
	if !started || attempt != 1 {
		t.Fatalf("first connection launch = (attempt %d, started %t), want (1, true)", attempt, started)
	}

	attempt, _, started = client.tryStartConnect()
	if started || attempt != 1 {
		t.Fatalf("overlapping connection launch = (attempt %d, started %t), want (1, false)", attempt, started)
	}

	client.releaseConnect(1)
	attempt, _, started = client.tryStartConnect()
	if !started || attempt != 1 {
		t.Fatalf("connection launch after release = (attempt %d, started %t), want (1, true)", attempt, started)
	}
}

func TestShouldCreateEmptyRoomsOnlyForExplicitFullSync(t *testing.T) {
	client := &MyNetworkClient{}
	if client.shouldCreateEmptyRooms() {
		t.Fatal("empty-room recreation should be disabled by default")
	}

	client.syncMu.Lock()
	client.fullSyncCreateEmpty = true
	client.syncMu.Unlock()
	if !client.shouldCreateEmptyRooms() {
		t.Fatal("empty-room recreation should be enabled for explicit full sync")
	}

	client.clearFullSyncState()
	if client.shouldCreateEmptyRooms() {
		t.Fatal("empty-room recreation should be cleared after sync")
	}
}

func TestWaitForConnectedReloadsAfterStateChange(t *testing.T) {
	client := &MyNetworkClient{connectionChanged: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := make(chan bool, 1)
	go func() {
		result <- client.WaitForConnected(ctx)
	}()

	client.connectMu.Lock()
	client.signalConnectionChangeLocked()
	client.connected = true
	client.signalConnectionChangeLocked()
	client.connectMu.Unlock()

	select {
	case connected := <-result:
		if !connected {
			t.Fatal("WaitForConnected returned false after connection became ready")
		}
	case <-ctx.Done():
		t.Fatal("WaitForConnected did not observe connection state changes")
	}
}

func TestDisconnectInvalidatesInFlightConnect(t *testing.T) {
	client := &MyNetworkClient{}
	_, generation, started := client.tryStartConnect()
	if !started {
		t.Fatal("initial connection launch did not start")
	}

	client.Disconnect()
	client.releaseConnect(generation)

	client.connectMu.Lock()
	defer client.connectMu.Unlock()
	if client.connectionGeneration == generation {
		t.Fatal("Disconnect did not invalidate the in-flight connection generation")
	}
	if client.connectStarted {
		t.Fatal("stale connection release changed lifecycle state after Disconnect")
	}
}

func TestIsXMPPAuthenticationError(t *testing.T) {
	for _, test := range []struct {
		name string
		err  string
		want bool
	}{
		{name: "auth failure", err: "auth failure: not-authorized", want: true},
		{name: "scram failure", err: "scram auth failure: invalid credentials", want: true},
		{name: "unsupported mechanism", err: "no matching authentication mechanism", want: true},
		{name: "closed connection", err: "connection closed", want: false},
		{name: "network timeout", err: "dial tcp: i/o timeout", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := isXMPPAuthenticationError(fmt.Errorf("%s", test.err)); got != test.want {
				t.Fatalf("isXMPPAuthenticationError(%q) = %t, want %t", test.err, got, test.want)
			}
		})
	}
}

func TestPingXMPPNotConnected(t *testing.T) {
	client := &MyNetworkClient{}
	_, err := client.PingXMPP(context.Background())
	if err == nil {
		t.Fatal("expected error from PingXMPP on unconnected client, got nil")
	}

	err = client.RuntimeHealth(context.Background())
	if err == nil {
		t.Fatal("expected error from RuntimeHealth on unconnected client, got nil")
	}
}

func TestTrackAndResolveOutboundAck(t *testing.T) {
	nc := &MyNetworkClient{}
	nc.trackOutboundSend("stanza-1", "+15551234567@cheogram.com", id.RoomID("!room:example.com"), "evt-1")

	if !nc.resolveOutboundAck("stanza-1", "delivery", "+15551234567@cheogram.com") {
		t.Fatal("expected outbound ack to resolve by stanza ID")
	}
	if nc.resolveOutboundAck("stanza-1", "delivery", "+15551234567@cheogram.com") {
		t.Fatal("expected resolution to be idempotent")
	}
	if nc.resolveOutboundAck("evt-1", "read", "+15551234567@cheogram.com") {
		t.Fatal("expected no resolution after the tracked entry was already removed")
	}
}

func TestResolveOutboundAckByMatrixEventID(t *testing.T) {
	nc := &MyNetworkClient{}
	nc.trackOutboundSend("stanza-2", "+15559876543@cheogram.com", id.RoomID("!room:example.com"), "evt-2")

	if !nc.resolveOutboundAck("evt-2", "delivery", "+15559876543@cheogram.com") {
		t.Fatal("expected outbound ack to resolve by Matrix event ID")
	}
	if nc.resolveOutboundAck("evt-2", "delivery", "+15559876543@cheogram.com") {
		t.Fatal("expected resolution to be idempotent")
	}
}

func TestScanPendingSendTimeouts(t *testing.T) {
	oldTimeout := xmppPendingSendAckTimeout
	xmppPendingSendAckTimeout = 5 * time.Minute
	t.Cleanup(func() { xmppPendingSendAckTimeout = oldTimeout })

	nc := &MyNetworkClient{}
	nc.pendingSends = map[string]*pendingXMPPSend{
		"stale-1": {
			targetJID:     "+15551112222@cheogram.com",
			portalRoomID:  id.RoomID("!room:example.com"),
			matrixEventID: "evt-stale-1",
			started:       time.Now().Add(-10 * time.Minute),
		},
		"fresh-1": {
			targetJID:     "+15553334444@cheogram.com",
			matrixEventID: "evt-fresh-1",
			started:       time.Now(),
		},
	}

	nc.scanPendingSendTimeouts()

	nc.pendingSendsMu.Lock()
	_, stillStale := nc.pendingSends["stale-1"]
	_, stillFresh := nc.pendingSends["fresh-1"]
	nc.pendingSendsMu.Unlock()

	if stillStale {
		t.Fatal("stale outbound send was not removed by timeout scan")
	}
	if !stillFresh {
		t.Fatal("fresh outbound send was incorrectly removed by timeout scan")
	}
}

func TestNotifyManagementSafeWhenUnconfigured(t *testing.T) {
	nc := &MyNetworkClient{}
	// Should not panic or crash when login or management room is missing
	nc.notifyManagement("test message")

	nc.login = &bridgev2.UserLogin{User: &bridgev2.User{}}
	nc.notifyManagement("test message with empty room")
}

func TestHandleMatrixMessageFailsWhenUnconnected(t *testing.T) {
	nc := &MyNetworkClient{}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	portal := &bridgev2.Portal{
		Portal: &database.Portal{
			PortalKey: networkid.PortalKey{
				ID: networkid.PortalID("+15551234567@cheogram.com"),
			},
		},
	}
	msg := &bridgev2.MatrixMessage{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.MessageEventContent]{
			Portal: portal,
			Event: &event.Event{
				ID:     "$test-event",
				Sender: "@user:example.com",
			},
			Content: &event.MessageEventContent{
				MsgType: event.MsgText,
				Body:    "hello world",
			},
		},
	}

	_, err := nc.HandleMatrixMessage(ctx, msg)
	if err == nil {
		t.Fatal("expected error when handling Matrix message while disconnected")
	}
}

func TestResolveOutboundError(t *testing.T) {
	nc := &MyNetworkClient{}
	nc.trackOutboundSend("err-stanza-1", "+15550001111@cheogram.com", id.RoomID("!room:example.com"), "evt-err-1")

	errPayload := stanza.Err{
		Type:   stanza.ErrorTypeCancel,
		Code:   503,
		Reason: "service-unavailable",
		Text:   "Destination carrier rejected SMS",
	}

	if !nc.resolveOutboundError("err-stanza-1", "+15550001111@cheogram.com", errPayload) {
		t.Fatal("expected outbound error to resolve by stanza ID")
	}

	// Should be removed from pendingSends
	nc.pendingSendsMu.Lock()
	_, exists := nc.pendingSends["err-stanza-1"]
	nc.pendingSendsMu.Unlock()
	if exists {
		t.Fatal("pending send should be removed after resolving error")
	}

	// Test fallback resolution by matching target JID when stanza ID differs or is missing
	nc.trackOutboundSend("err-stanza-2", "+15550002222@cheogram.com", id.RoomID("!room:example.com"), "evt-err-2")
	if !nc.resolveOutboundError("unknown-id", "+15550002222@cheogram.com", errPayload) {
		t.Fatal("expected outbound error to resolve by target JID fallback")
	}
}

func TestStripMUCSenderPrefix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		body     string
		senderID string
		want     string
	}{
		{
			name:     "standard phone prefix with colon",
			body:     "+15551230001: Hello everyone!",
			senderID: "+15551230001@cheogram.com",
			want:     "Hello everyone!",
		},
		{
			name:     "standard phone prefix with space",
			body:     "+15551230001 Hello everyone!",
			senderID: "+15551230001@cheogram.com",
			want:     "Hello everyone!",
		},
		{
			name:     "xmpp uri bracketed prefix",
			body:     "<xmpp:+15551230001@cheogram.com> Hello group!",
			senderID: "+15551230001@cheogram.com",
			want:     "Hello group!",
		},
		{
			name:     "xmpp uri prefix with colon",
			body:     "xmpp:+15551230001@cheogram.com: Hello group!",
			senderID: "+15551230001@cheogram.com",
			want:     "Hello group!",
		},
		{
			name:     "full JID prefix with colon",
			body:     "+15551230001@cheogram.com: Hello group!",
			senderID: "+15551230001@cheogram.com",
			want:     "Hello group!",
		},
		{
			name:     "occupant nickname prefix",
			body:     "Alice: Let's meet at 5",
			senderID: "conference@server.com/Alice",
			want:     "Let's meet at 5",
		},
		{
			name:     "no prefix matches",
			body:     "Just a regular message",
			senderID: "+15551230001@cheogram.com",
			want:     "Just a regular message",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripMUCSenderPrefix(tt.body, tt.senderID); got != tt.want {
				t.Errorf("stripMUCSenderPrefix(%q, %q) = %q, want %q", tt.body, tt.senderID, got, tt.want)
			}
		})
	}
}

func TestExtractPrefixedSender(t *testing.T) {
	t.Parallel()

	jid, stripped, ok := extractPrefixedSender("<xmpp:+15551230001@cheogram.com> Hello group!")
	if !ok {
		t.Fatal("expected extractPrefixedSender to succeed")
	}
	if jid != "+15551230001@cheogram.com" {
		t.Errorf("jid = %q, want %q", jid, "+15551230001@cheogram.com")
	}
	if stripped != "Hello group!" {
		t.Errorf("stripped = %q, want %q", stripped, "Hello group!")
	}

	_, _, ok = extractPrefixedSender("Hello group without prefix!")
	if ok {
		t.Fatal("expected extractPrefixedSender to return false for un-prefixed message")
	}
}
