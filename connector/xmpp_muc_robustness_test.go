// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"reflect"
	"testing"
	"time"

	"gosrc.io/xmpp/stanza"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
)

func TestMucJoinBackoff(t *testing.T) {
	t.Parallel()
	tests := []struct {
		attempts int
		want     time.Duration
	}{
		{attempts: 0, want: 30 * time.Second},
		{attempts: 1, want: 30 * time.Second},
		{attempts: 2, want: time.Minute},
		{attempts: 3, want: 2 * time.Minute},
		{attempts: 4, want: 4 * time.Minute},
		{attempts: 5, want: 8 * time.Minute},
		{attempts: 6, want: 10 * time.Minute},
		{attempts: 10, want: 10 * time.Minute},
	}
	for _, tc := range tests {
		if got := mucJoinBackoff(tc.attempts); got != tc.want {
			t.Errorf("mucJoinBackoff(%d) = %s, want %s", tc.attempts, got, tc.want)
		}
	}
}

func TestDeriveMUCNickAlternatives(t *testing.T) {
	t.Parallel()
	tests := []struct {
		base string
		max  int
		want []string
	}{
		{base: "matrix", max: 3, want: []string{"matrix_1", "matrix_2", "matrix_3"}},
		{base: " matrix ", max: 1, want: []string{"matrix_1"}},
		{base: "", max: 3, want: nil},
		{base: "matrix", max: 0, want: nil},
		{base: "   ", max: 3, want: nil},
	}
	for _, tc := range tests {
		got := deriveMUCNickAlternatives(tc.base, tc.max)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("deriveMUCNickAlternatives(%q, %d) = %v, want %v", tc.base, tc.max, got, tc.want)
		}
	}
}

func TestIsNickConflictPresence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		pres stanza.Presence
		want bool
	}{
		{name: "code 409", pres: stanza.Presence{Type: stanza.PresenceTypeError, Error: stanza.Err{Code: 409}}, want: true},
		{name: "reason conflict", pres: stanza.Presence{Type: stanza.PresenceTypeError, Error: stanza.Err{Reason: "conflict"}}, want: true},
		{name: "reason conflict case-insensitive", pres: stanza.Presence{Type: stanza.PresenceTypeError, Error: stanza.Err{Reason: "Conflict"}}, want: true},
		{name: "other error code", pres: stanza.Presence{Type: stanza.PresenceTypeError, Error: stanza.Err{Code: 404}}, want: false},
		{name: "unavailable", pres: stanza.Presence{Type: stanza.PresenceTypeUnavailable}, want: false},
		{name: "available", pres: stanza.Presence{}, want: false},
	}
	for _, tc := range tests {
		if got := isNickConflictPresence(tc.pres); got != tc.want {
			t.Errorf("%s: isNickConflictPresence() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestMucNickForRoomOverridePrecedence(t *testing.T) {
	t.Parallel()
	nc := &MyNetworkClient{login: &bridgev2.UserLogin{
		UserLogin: &database.UserLogin{
			Metadata: &LoginMetadata{MUCNickOverrides: map[string]string{"room@conf": "custom"}},
		},
	}}
	if got := nc.mucNickForRoom("room@conf", "bookmark"); got != "custom" {
		t.Errorf("override should win, got %q", got)
	}
	if got := nc.mucNickForRoom("other@conf", "bookmark"); got != "bookmark" {
		t.Errorf("bookmark nick should be used without override, got %q", got)
	}
	if got := nc.mucNickForRoom("other@conf", ""); got != "matrix" {
		t.Errorf("default nick should be used as fallback, got %q", got)
	}
}

func TestParseBookmarkPEPEvent(t *testing.T) {
	t.Parallel()
	inner := `<items node='urn:xmpp:bookmarks:1'>` +
		`<item id='alice@conf'><conference xmlns='urn:xmpp:bookmarks:1' jid='alice@conf' name='Alice Room' autojoin='true'><nick>alice_nick</nick></conference></item>` +
		`<item id='bob@conf'><retract/></item>` +
		`</items>`
	conferences, removed, ok := parseBookmarkPEPEvent(inner)
	if !ok {
		t.Fatal("parseBookmarkPEPEvent() ok = false, want true")
	}
	wantConferences := []BookmarkConference{{JID: "alice@conf", Name: "Alice Room", Autojoin: "true", Nick: "alice_nick"}}
	if !reflect.DeepEqual(conferences, wantConferences) {
		t.Errorf("conferences = %#v, want %#v", conferences, wantConferences)
	}
	wantRemoved := []string{"bob@conf"}
	if !reflect.DeepEqual(removed, wantRemoved) {
		t.Errorf("removed = %v, want %v", removed, wantRemoved)
	}

	otherNode := `<items node='some.other.node'><item id='a@conf'><conference xmlns='urn:xmpp:bookmarks:1' jid='a@conf'/></item></items>`
	if _, _, ok := parseBookmarkPEPEvent(otherNode); ok {
		t.Error("parseBookmarkPEPEvent() for other node ok = true, want false")
	}
	if _, _, ok := parseBookmarkPEPEvent(""); ok {
		t.Error("parseBookmarkPEPEvent() for empty input ok = true, want false")
	}
	if _, _, ok := parseBookmarkPEPEvent("not xml"); ok {
		t.Error("parseBookmarkPEPEvent() for invalid XML ok = true, want false")
	}
}

func TestExtractMUCSubject(t *testing.T) {
	t.Parallel()
	withSubject := decodeTestMessage(t, `<message from="room@conference.example.com" type="groupchat">`+
		`<subject>New topic</subject>`+
		`</message>`)
	subject, ok := extractMUCSubject(withSubject)
	if !ok || subject != "New topic" {
		t.Errorf("extractMUCSubject() = %q, %v, want %q, true", subject, ok, "New topic")
	}

	emptySubject := decodeTestMessage(t, `<message from="room@conference.example.com" type="groupchat">`+
		`<subject>   </subject>`+
		`</message>`)
	if subject, ok := extractMUCSubject(emptySubject); ok || subject != "" {
		t.Errorf("extractMUCSubject() for whitespace = %q, %v, want empty, false", subject, ok)
	}

	noSubject := decodeTestMessage(t, `<message from="room@conference.example.com" type="groupchat"/>`)
	if subject, ok := extractMUCSubject(noSubject); ok || subject != "" {
		t.Errorf("extractMUCSubject() without subject = %q, %v, want empty, false", subject, ok)
	}
}
