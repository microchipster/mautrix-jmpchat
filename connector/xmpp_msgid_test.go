// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"strings"
	"testing"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/bridgeconfig"
	"maunium.net/go/mautrix/bridgev2/database"
)

func TestInboundMessageIDPrecedence(t *testing.T) {
	t.Parallel()
	// The id attribute wins over everything else.
	if got := inboundMessageID(inboundIDs{idAttr: "a", stanzaID: "s", scope: "x", originID: "o"}); got != "a" {
		t.Fatalf("id attribute should win: got %q", got)
	}
	// A trusted stanza-id is used scoped by its assigner (lowercased).
	got := inboundMessageID(inboundIDs{stanzaID: "s", scope: "Room@Conf.Example", originID: "o"})
	if want := "xmp:sid:room@conf.example:s"; string(got) != want {
		t.Fatalf("stanza-id id mismatch: got %q, want %q", got, want)
	}
	// A stanza-id without a scope is not trusted: the origin-id is used.
	if got := inboundMessageID(inboundIDs{stanzaID: "s", originID: "o"}); got != "o" {
		t.Fatalf("origin-id should be used without a scope: got %q", got)
	}
	if got := inboundMessageID(inboundIDs{originID: "o"}); got != "o" {
		t.Fatalf("origin-id mismatch: got %q", got)
	}
}

func TestInboundMessageIDHashStability(t *testing.T) {
	t.Parallel()
	delay := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	base := inboundIDs{
		scope:      "room@conf.example",
		delay:      delay,
		senderFull: "room@conf.example/alice",
		body:       "hello",
	}
	first := inboundMessageID(base)
	second := inboundMessageID(base)
	if first != second {
		t.Fatalf("hash ids must be stable: %q vs %q", first, second)
	}
	if !strings.HasPrefix(string(first), msgIDHashPrefix) {
		t.Fatalf("expected xmp:h: prefix, got %q", first)
	}
	changed := base
	changed.body = "hello2"
	if inboundMessageID(changed) == first {
		t.Fatal("different bodies must produce different hash ids")
	}
	changed = base
	changed.senderFull = "room@conf.example/bob"
	if inboundMessageID(changed) == first {
		t.Fatal("different senders must produce different hash ids")
	}
}

func TestInboundMessageIDRandomFallback(t *testing.T) {
	t.Parallel()
	first := inboundMessageID(inboundIDs{})
	second := inboundMessageID(inboundIDs{})
	if !strings.HasPrefix(string(first), msgIDRandPrefix) || !strings.HasPrefix(string(second), msgIDRandPrefix) {
		t.Fatalf("expected xmp:r: prefixes, got %q and %q", first, second)
	}
	if first == second {
		t.Fatal("random ids must differ")
	}
}

func TestTrustedDelay(t *testing.T) {
	t.Parallel()
	nc := &MyNetworkClient{}
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	dm := func(raw string) time.Time {
		return nc.trustedDelay(decodeTestMessage(t, raw), "", "15550101100@cheogram.com", false, now)
	}
	// Delays without a from are trusted in DMs.
	if got := dm(`<message from="15550101100@cheogram.com" type="chat"><delay xmlns="urn:xmpp:delay" stamp="2026-01-01T00:00:00Z"/></message>`); got.IsZero() {
		t.Fatal("delay without from should be trusted in a DM")
	}
	// Delays stamped by the sender's domain are trusted.
	if got := dm(`<message from="15550101100@cheogram.com" type="chat"><delay xmlns="urn:xmpp:delay" from="cheogram.com" stamp="2026-01-01T00:00:00Z"/></message>`); got.IsZero() {
		t.Fatal("delay from the sender domain should be trusted")
	}
	// Delays from foreign domains are not.
	if got := dm(`<message from="15550101100@cheogram.com" type="chat"><delay xmlns="urn:xmpp:delay" from="evil.example" stamp="2026-01-01T00:00:00Z"/></message>`); !got.IsZero() {
		t.Fatal("delay from a foreign domain must not be trusted")
	}
	// Bogus (too old) stamps are ignored.
	if got := dm(`<message from="15550101100@cheogram.com" type="chat"><delay xmlns="urn:xmpp:delay" stamp="1999-01-01T00:00:00Z"/></message>`); !got.IsZero() {
		t.Fatal("pre-2000 stamps must be ignored")
	}
	// Future stamps are clamped to now.
	raw := `<message from="15550101100@cheogram.com" type="chat"><delay xmlns="urn:xmpp:delay" stamp="2027-01-01T00:00:00Z"/></message>`
	if got := dm(raw); !got.Equal(now) {
		t.Fatalf("future stamps should clamp to now: got %s", got)
	}

	muc := func(raw string) time.Time {
		return nc.trustedDelay(decodeTestMessage(t, raw), "room@conference.example.com", "room@conference.example.com/alice", true, now)
	}
	if got := muc(`<message from="room@conference.example.com/alice" type="groupchat"><delay xmlns="urn:xmpp:delay" from="room@conference.example.com" stamp="2026-01-01T00:00:00Z"/></message>`); got.IsZero() {
		t.Fatal("room delays should be trusted in MUCs")
	}
	if got := muc(`<message from="room@conference.example.com/alice" type="groupchat"><delay xmlns="urn:xmpp:delay" from="alice.example" stamp="2026-01-01T00:00:00Z"/></message>`); !got.IsZero() {
		t.Fatal("participant delays must not be trusted in MUCs")
	}
	if got := muc(`<message from="room@conference.example.com/alice" type="groupchat"><x xmlns="jabber:x:delay" stamp="20260101T00:00:00"/></message>`); !got.IsZero() {
		t.Fatal("legacy delays must not be trusted in MUCs")
	}
}

func TestDelayDomain(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"user@example.com/resource": "example.com",
		"example.com":               "example.com",
		"USER@Example.COM":          "example.com",
		"user@cheogram.com":         "cheogram.com",
	}
	for in, want := range cases {
		if got := delayDomain(in); got != want {
			t.Errorf("delayDomain(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEffectivePortalKey(t *testing.T) {
	t.Parallel()
	// No bridge: the bare ID is returned.
	nc := &MyNetworkClient{}
	if got := nc.effectivePortalKey("p1"); got.ID != "p1" || got.Receiver != "" {
		t.Fatalf("bare key mismatch: %+v", got)
	}
	// Split portals: the login ID is the receiver.
	login := &bridgev2.UserLogin{UserLogin: &database.UserLogin{ID: "login1"}}
	nc = &MyNetworkClient{
		login:  login,
		bridge: &bridgev2.Bridge{Config: &bridgeconfig.BridgeConfig{SplitPortals: true}},
	}
	if got := nc.effectivePortalKey("p1"); got.ID != "p1" || got.Receiver != "login1" {
		t.Fatalf("split-portal key mismatch: %+v", got)
	}
	// Non-split: an empty receiver (nothing cached).
	nc.bridge.Config.SplitPortals = false
	if got := nc.effectivePortalKey("p1"); got.ID != "p1" || got.Receiver != "" {
		t.Fatalf("non-split key mismatch: %+v", got)
	}
}
