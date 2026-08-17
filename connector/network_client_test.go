// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"fmt"
	"testing"
	"time"
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
