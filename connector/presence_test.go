// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"testing"

	"github.com/rs/zerolog"
	"gosrc.io/xmpp/stanza"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
)

func TestIsCheogramGroupRoomJID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		jid  string
		want bool
	}{
		{name: "two contacts", jid: "+15550101300,+15550101400@cheogram.com", want: true},
		{name: "four contacts", jid: "+15550101600,+15550101700,+15550101800,+15550101900@cheogram.com", want: true},
		{name: "single contact", jid: "+15550101300@cheogram.com", want: false},
		{name: "regular muc", jid: "room@conference.example.org", want: false},
		{name: "comma other host", jid: "+15550101300,+15550101400@example.org", want: false},
		{name: "empty", jid: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isCheogramGroupRoomJID(tt.jid); got != tt.want {
				t.Errorf("isCheogramGroupRoomJID(%q) = %t, want %t", tt.jid, got, tt.want)
			}
		})
	}
}

func TestIsSubscriptionPresenceType(t *testing.T) {
	t.Parallel()

	for _, presType := range []stanza.StanzaType{
		stanza.PresenceTypeSubscribe,
		stanza.PresenceTypeSubscribed,
		stanza.PresenceTypeUnsubscribe,
		stanza.PresenceTypeUnsubscribed,
	} {
		if !isSubscriptionPresenceType(presType) {
			t.Errorf("isSubscriptionPresenceType(%q) = false, want true", presType)
		}
	}
	for _, presType := range []stanza.StanzaType{
		"",
		stanza.PresenceTypeProbe,
		stanza.PresenceTypeUnavailable,
		stanza.PresenceTypeError,
	} {
		if isSubscriptionPresenceType(presType) {
			t.Errorf("isSubscriptionPresenceType(%q) = true, want false", presType)
		}
	}
}

func TestResolveMUCPresenceMemberJID(t *testing.T) {
	t.Parallel()

	groupRoom := "+15550101300,+15550101400@cheogram.com"
	regularRoom := "room@conference.example.org"

	tests := []struct {
		name     string
		roomJID  string
		from     string
		resource string
		mucUser  *MUCUserInvite
		want     string
	}{
		{
			name:     "cheogram group phone resource becomes contact",
			roomJID:  groupRoom,
			from:     groupRoom + "/+15550101300",
			resource: "+15550101300",
			want:     "+15550101300@cheogram.com",
		},
		{
			name:     "cheogram group tel pseudo-resource is ignored",
			roomJID:  groupRoom,
			from:     groupRoom + "/tel",
			resource: "tel",
			want:     "",
		},
		{
			name:     "cheogram group nickname resource is ignored",
			roomJID:  groupRoom,
			from:     groupRoom + "/Alice",
			resource: "Alice",
			want:     "",
		},
		{
			name:     "cheogram group muc item jid wins",
			roomJID:  groupRoom,
			from:     groupRoom + "/whatever",
			resource: "whatever",
			mucUser:  &MUCUserInvite{Item: &MUCUserItem{JID: "+15550101400@cheogram.com/tel"}},
			want:     "+15550101400@cheogram.com",
		},
		{
			name:     "cheogram group muc item phone nick becomes contact",
			roomJID:  groupRoom,
			from:     groupRoom + "/x",
			resource: "x",
			mucUser:  &MUCUserInvite{Item: &MUCUserItem{Nick: "+15550101500"}},
			want:     "+15550101500@cheogram.com",
		},
		{
			name:     "cheogram group muc item plain nick is ignored",
			roomJID:  groupRoom,
			from:     groupRoom + "/x",
			resource: "x",
			mucUser:  &MUCUserInvite{Item: &MUCUserItem{Nick: "Bob"}},
			want:     "",
		},
		{
			name:     "regular muc resource keeps occupant address",
			roomJID:  regularRoom,
			from:     regularRoom + "/alice",
			resource: "alice",
			want:     regularRoom + "/alice",
		},
		{
			name:     "regular muc item jid wins",
			roomJID:  regularRoom,
			from:     regularRoom + "/whatever",
			resource: "whatever",
			mucUser:  &MUCUserInvite{Item: &MUCUserItem{JID: "alice@example.org"}},
			want:     "alice@example.org",
		},
		{
			name:    "regular muc without resource uses sender",
			roomJID: regularRoom,
			from:    regularRoom,
			want:    regularRoom,
		},
		{
			name:    "empty presence falls back to room jid",
			roomJID: regularRoom,
			want:    regularRoom,
		},
	}

	nc := &MyNetworkClient{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pres := stanza.Presence{Attrs: stanza.Attrs{From: tt.from}}
			if got := nc.resolveMUCPresenceMemberJID(tt.roomJID, pres, tt.resource, tt.mucUser); got != tt.want {
				t.Errorf("resolveMUCPresenceMemberJID(%q, %q, %q) = %q, want %q", tt.roomJID, tt.from, tt.resource, got, tt.want)
			}
		})
	}
}

func TestApplyMUCOccupantPresence(t *testing.T) {
	t.Parallel()

	room := "room@conference.example.org"
	fullFrom := room + "/+15550101300"

	occupants := map[string]string{}

	applyMUCOccupantPresence(occupants, fullFrom, "+15550101300@cheogram.com", false)
	if occupants[fullFrom] != "+15550101300@cheogram.com" {
		t.Fatalf("occupant was not recorded: %#v", occupants)
	}

	applyMUCOccupantPresence(occupants, room+"/tel", "", false)
	if _, ok := occupants[room+"/tel"]; ok {
		t.Fatal("non-participant resource must not be recorded as occupant")
	}

	applyMUCOccupantPresence(occupants, fullFrom, "", false)
	if _, ok := occupants[fullFrom]; ok {
		t.Fatal("occupant must be removed when the resolution becomes empty")
	}

	applyMUCOccupantPresence(occupants, fullFrom, "+15550101300@cheogram.com", false)
	applyMUCOccupantPresence(occupants, fullFrom, "", true)
	if _, ok := occupants[fullFrom]; ok {
		t.Fatal("unavailable presence must remove the occupant")
	}
}

func TestBuildMUCMemberMap(t *testing.T) {
	t.Parallel()

	nc := &MyNetworkClient{login: &bridgev2.UserLogin{UserLogin: &database.UserLogin{}}}
	nc.login.Metadata = &LoginMetadata{RemoteUserID: "alice@example.com"}

	groupRoom := "+15550101300,+15550101400@cheogram.com"
	occupants := map[string]string{
		groupRoom + "/+15550101300": "+15550101300@cheogram.com",
		groupRoom + "/tel":          "",
		groupRoom + "/other":        "alice@example.com",
	}

	memberMap := nc.buildMUCMemberMap(groupRoom, occupants)
	if len(memberMap) != 3 {
		t.Fatalf("member count = %d, want 3 (self + two contacts): %#v", len(memberMap), memberMap)
	}
	if member, ok := memberMap[""]; !ok || !member.IsFromMe {
		t.Fatalf("self member missing or not marked as from me: %#v", member)
	}
	for _, contactJID := range []string{"+15550101300@cheogram.com", "+15550101400@cheogram.com"} {
		key := MakeUserID(contactJID)
		member, ok := memberMap[key]
		if !ok {
			t.Errorf("contact %s missing from member map", contactJID)
			continue
		}
		if member.Sender != key {
			t.Errorf("contact %s sender = %q, want %q", contactJID, member.Sender, key)
		}
	}
	if _, ok := memberMap[MakeUserID("alice@example.com")]; ok {
		t.Error("own bare JID must not be part of the member map")
	}

	regularRoom := "room@conference.example.org"
	memberMap = nc.buildMUCMemberMap(regularRoom, map[string]string{
		regularRoom + "/alice": "alice@example.org",
	})
	if len(memberMap) != 2 {
		t.Fatalf("regular room member count = %d, want 2 (self + alice): %#v", len(memberMap), memberMap)
	}
	if _, ok := memberMap[MakeUserID("alice@example.org")]; !ok {
		t.Error("regular MUC occupant missing from member map")
	}
}

func TestChatMemberMapSnapshot(t *testing.T) {
	t.Parallel()

	first := bridgev2.ChatMemberMap{
		"":                                      {},
		MakeUserID("+15550101300@cheogram.com"): {},
	}
	second := bridgev2.ChatMemberMap{
		MakeUserID("+15550101300@cheogram.com"): {},
		"":                                      {},
	}
	if chatMemberMapSnapshot(first) != chatMemberMapSnapshot(second) {
		t.Fatal("snapshot must be stable regardless of map iteration order")
	}

	second[MakeUserID("+15550101400@cheogram.com")] = bridgev2.ChatMember{}
	if chatMemberMapSnapshot(first) == chatMemberMapSnapshot(second) {
		t.Fatal("snapshot must change when the member set changes")
	}
}

func TestHandleXMPPPresenceIgnoresMUCSubscriptionPresence(t *testing.T) {
	t.Parallel()

	nc := &MyNetworkClient{log: zerolog.Nop()}
	groupRoom := "+15550101300,+15550101400@cheogram.com"

	for _, presType := range []stanza.StanzaType{
		stanza.PresenceTypeSubscribe,
		stanza.PresenceTypeSubscribed,
		stanza.PresenceTypeUnsubscribe,
		stanza.PresenceTypeUnsubscribed,
	} {
		pres := stanza.Presence{Attrs: stanza.Attrs{From: groupRoom, Type: presType}}
		nc.handleXMPPPresence(nil, pres)
	}

	if nc.mucOccupants != nil {
		t.Fatalf("subscription presences must not create occupants: %#v", nc.mucOccupants)
	}
	if nc.mucMemberSnapshot != nil {
		t.Fatalf("subscription presences must not record a member snapshot: %#v", nc.mucMemberSnapshot)
	}
}
