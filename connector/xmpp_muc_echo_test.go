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

func testEchoClient() *MyNetworkClient {
	return &MyNetworkClient{mucEchoes: map[string]mucEchoEntry{}}
}

func TestMucEchoRecordAndLookup(t *testing.T) {
	t.Parallel()

	client := testEchoClient()

	if entry, ok := client.lookupMucEcho("origin-1"); ok {
		t.Fatalf("expected no echo for unknown origin id, got %+v", entry)
	}

	client.recordMucEcho("origin-1", "room-stanza-1", "occ-1")
	entry, ok := client.lookupMucEcho("origin-1")
	if !ok {
		t.Fatal("expected echo to be recorded")
	}
	if entry.stanzaID != "room-stanza-1" || entry.occupantID != "occ-1" {
		t.Fatalf("unexpected echo entry: %+v", entry)
	}

	// A partial echo must not erase identifiers learned before.
	client.recordMucEcho("origin-1", "", "occ-2")
	entry, ok = client.lookupMucEcho("origin-1")
	if !ok || entry.stanzaID != "room-stanza-1" || entry.occupantID != "occ-1" {
		t.Fatalf("partial echo overwrote existing identifiers: %+v", entry)
	}

	// Completely empty echoes are ignored.
	client.recordMucEcho("origin-2", "  ", "")
	if _, ok := client.lookupMucEcho("origin-2"); ok {
		t.Fatal("empty echo should not be recorded")
	}

	// Empty origin ids are ignored.
	client.recordMucEcho("", "sid", "occ")
	if len(client.mucEchoes) != 1 {
		t.Fatalf("expected exactly one recorded echo, got %d", len(client.mucEchoes))
	}
}

func TestMucEchoTTLExpiry(t *testing.T) {
	t.Parallel()

	client := testEchoClient()
	client.recordMucEcho("origin-1", "stanza-1", "")

	client.mucEchoMu.Lock()
	entry := client.mucEchoes["origin-1"]
	entry.at = time.Now().Add(-2 * mucEchoTTL)
	client.mucEchoes["origin-1"] = entry
	client.mucEchoMu.Unlock()

	if _, ok := client.lookupMucEcho("origin-1"); ok {
		t.Fatal("expired echo should not be returned")
	}
	client.mucEchoMu.Lock()
	_, exists := client.mucEchoes["origin-1"]
	client.mucEchoMu.Unlock()
	if exists {
		t.Fatal("expired echo should be deleted on lookup")
	}
}

func TestMucEchoNilMapSafety(t *testing.T) {
	t.Parallel()

	client := &MyNetworkClient{}
	client.recordMucEcho("origin-1", "stanza-1", "occ-1")
	if _, ok := client.lookupMucEcho("origin-1"); ok {
		t.Fatal("nil map should not record echoes")
	}
	if _, ok := client.waitMucEcho(context.Background(), "origin-1", 10*time.Millisecond); ok {
		t.Fatal("nil map should not wait for echoes")
	}
}

func TestWaitMucEchoImmediate(t *testing.T) {
	t.Parallel()

	client := testEchoClient()
	client.recordMucEcho("origin-1", "room-stanza-1", "")

	start := time.Now()
	entry, ok := client.waitMucEcho(context.Background(), "origin-1", time.Second)
	if !ok || entry.stanzaID != "room-stanza-1" {
		t.Fatalf("expected immediate echo, got %+v ok=%v", entry, ok)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("immediate echo took too long: %v", elapsed)
	}
}

func TestWaitMucEchoTimeout(t *testing.T) {
	t.Parallel()

	client := testEchoClient()
	if _, ok := client.waitMucEcho(context.Background(), "origin-missing", 50*time.Millisecond); ok {
		t.Fatal("expected wait to time out")
	}
}

func TestWaitMucEchoContextCancelled(t *testing.T) {
	t.Parallel()

	client := testEchoClient()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	if _, ok := client.waitMucEcho(ctx, "origin-missing", time.Second); ok {
		t.Fatal("expected wait to abort on cancelled context")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("cancelled wait took too long: %v", elapsed)
	}
}

func TestMucEchoMapCap(t *testing.T) {
	t.Parallel()

	client := testEchoClient()
	for i := 0; i < mucEchoMapCap+10; i++ {
		client.recordMucEcho(fmt.Sprintf("origin-%d", i), "sid", "")
	}
	client.mucEchoMu.Lock()
	size := len(client.mucEchoes)
	client.mucEchoMu.Unlock()
	if size > mucEchoMapCap {
		t.Fatalf("echo map exceeded cap: %d", size)
	}
	// Oldest entries should have been evicted, newest must survive.
	if _, ok := client.lookupMucEcho(fmt.Sprintf("origin-%d", mucEchoMapCap+9)); !ok {
		t.Fatal("newest echo should survive eviction")
	}
}
