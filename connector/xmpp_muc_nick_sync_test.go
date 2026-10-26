// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"strings"
	"testing"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func TestSanitizeMUCNick(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{ // raw -> acceptable
		"alice":                 true,
		"Alice_2":               true,
		"O'Brien":               true,
		"":                      false,
		"two words":             false,
		"has\ttab":              false,
		strings.Repeat("x", 65): false,
		"has@at":                false,
		"has/slash":             false,
		"has,comma":             false,
	}
	for raw, ok := range cases {
		nick, reason := sanitizeMUCNick(raw)
		if ok && (nick == "" || reason != "") {
			t.Errorf("sanitizeMUCNick(%q) should be accepted: nick=%q reason=%q", raw, nick, reason)
		}
		if !ok && nick != "" {
			t.Errorf("sanitizeMUCNick(%q) should be rejected: nick=%q", raw, nick)
		}
	}
	if nick, reason := sanitizeMUCNick("O'Brien"); nick != "O'Brien" || reason != "" {
		t.Errorf("apostrophes must be accepted: nick=%q reason=%q", nick, reason)
	}
}

func TestNickSyncStateEcho(t *testing.T) {
	t.Parallel()
	var s nickSyncState
	room := id.RoomID("!room:example.com")
	s.recordOwnWrite(room, "Alice")
	if !s.consumeOwnWrite(room, "Alice") {
		t.Fatal("the recorded write should be consumed once")
	}
	if s.consumeOwnWrite(room, "Alice") {
		t.Fatal("the write must only be consumed once")
	}
	s.recordOwnWrite(room, "Bob")
	s.forgetOwnWrite(room, "Bob")
	if s.consumeOwnWrite(room, "Bob") {
		t.Fatal("a forgotten write must not be consumed")
	}
	// Expired writes are ignored.
	s.mu.Lock()
	s.writes[room] = ownMemberWrite{display: "Old", at: time.Now().Add(-3 * ownWriteTTL)}
	s.mu.Unlock()
	if s.consumeOwnWrite(room, "Old") {
		t.Fatal("expired writes must not be consumed")
	}
}

func TestNickSyncScheduleReplacesPending(t *testing.T) {
	t.Parallel()
	var s nickSyncState
	portal := networkid.PortalID("p1")
	first := &nickSyncReq{display: "One"}
	second := &nickSyncReq{display: "Two"}
	s.schedule(portal, first, time.Hour, func(*nickSyncReq) {})
	s.schedule(portal, second, time.Hour, func(*nickSyncReq) {})
	defer second.timer.Stop()
	defer first.timer.Stop()
	s.mu.Lock()
	got := s.pending[portal]
	s.mu.Unlock()
	if got != second {
		t.Fatal("the newer request must replace the pending one")
	}
}

func TestNickSyncPortalOK(t *testing.T) {
	t.Parallel()
	login := &bridgev2.UserLogin{UserLogin: &database.UserLogin{ID: "login1"}}
	nc := &MyNetworkClient{login: login}
	portal := func(id string, meta *PortalMetadata, roomType database.RoomType, mxid id.RoomID) *database.Portal {
		return &database.Portal{
			ID:       networkid.PortalID(id),
			MXID:     mxid,
			RoomType: roomType,
			Metadata: meta,
		}
	}
	if nc.nickSyncPortalOK(nil) {
		t.Fatal("nil portal")
	}
	if nc.nickSyncPortalOK(portal("room@conf.example.com", &PortalMetadata{IsMUC: true}, "", "")) {
		t.Fatal("portal without a Matrix room")
	}
	if nc.nickSyncPortalOK(portal("room@conf.example.com", &PortalMetadata{IsMUC: true}, "", "!r:example.com")) == false {
		t.Fatal("bridged MUC portal should be ok")
	}
	if nc.nickSyncPortalOK(portal("room@conf.example.com", &PortalMetadata{IsMUC: true, MUCLeft: true}, "", "!r:example.com")) {
		t.Fatal("a left room must be skipped")
	}
	if nc.nickSyncPortalOK(portal("room@conf.example.com", &PortalMetadata{IsMUC: true, MUCInvitePending: true}, "", "!r:example.com")) {
		t.Fatal("an invite-pending room must be skipped")
	}
	if nc.nickSyncPortalOK(portal("+15550101100@cheogram.com", nil, "", "!r:example.com")) {
		t.Fatal("Cheogram DM portals must be skipped")
	}
	if nc.nickSyncPortalOK(portal("alice@example.com", nil, database.RoomTypeDM, "!r:example.com")) {
		t.Fatal("DM portals must be skipped")
	}
	if !nc.nickSyncPortalOK(portal("room@conference.example.com", nil, "", "!r:example.com")) {
		t.Fatal("a room domain heuristic should be accepted")
	}
	if nc.nickSyncPortalOK(portal("alice@example.com", nil, "", "!r:example.com")) {
		t.Fatal("a plain contact JID must be skipped")
	}
}

func TestHandleOwnProfileChangeGuards(t *testing.T) {
	t.Parallel()
	login := &bridgev2.UserLogin{UserLogin: &database.UserLogin{ID: "login1", UserMXID: "@alice:example.com"}}
	nc := &MyNetworkClient{login: login}
	portal := &bridgev2.Portal{Portal: &database.Portal{
		ID:       MakePortalID("room@conference.example.com"),
		MXID:     "!room:example.com",
		Metadata: &PortalMetadata{IsMUC: true},
	}}
	nc.handleOwnProfileChange(t.Context(), nil) // must not panic

	// Not targeting the own login.
	nc.handleOwnProfileChange(t.Context(), &bridgev2.MatrixMembershipChange{
		Type:   bridgev2.ProfileChange,
		Target: &bridgev2.UserLogin{UserLogin: &database.UserLogin{ID: "login2"}},
	})
	// The echo of our own write is consumed, not scheduled.
	stateKey := "@alice:example.com"
	nc.nickSync.recordOwnWrite("!room:example.com", "NewName")
	nc.handleOwnProfileChange(t.Context(), &bridgev2.MatrixMembershipChange{
		Type:   bridgev2.ProfileChange,
		Target: login,
		MatrixRoomMeta: bridgev2.MatrixRoomMeta[*event.MemberEventContent]{
			MatrixEventBase: bridgev2.MatrixEventBase[*event.MemberEventContent]{
				Event:   &event.Event{Sender: "@alice:example.com", StateKey: &stateKey},
				Portal:  portal,
				Content: &event.MemberEventContent{Displayname: "NewName"},
			},
		},
	})
	nc.nickSync.mu.Lock()
	pending := len(nc.nickSync.pending)
	nc.nickSync.mu.Unlock()
	if pending != 0 {
		t.Fatalf("the own-write echo must not be scheduled: %d pending", pending)
	}
}

func TestHandleOwnMUCNickPresence(t *testing.T) {
	t.Parallel()
	nc := &MyNetworkClient{}
	// The first observed nick is recorded without any further action.
	nc.handleOwnMUCNickPresence("room@conf.example.com", "alice")
	if got := nc.mucNick["room@conf.example.com"]; got != "alice" {
		t.Fatalf("nick not recorded: %q", got)
	}
	// An unchanged nick is ignored.
	nc.handleOwnMUCNickPresence("room@conf.example.com", "alice")
	if got := nc.mucNick["room@conf.example.com"]; got != "alice" {
		t.Fatalf("nick changed unexpectedly: %q", got)
	}
	if nc.mucNick["room@conf.example.com"] != "alice" {
		t.Fatal("nick mismatch")
	}
}
