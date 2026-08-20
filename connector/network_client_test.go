// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"fmt"
	"testing"
	"time"

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
