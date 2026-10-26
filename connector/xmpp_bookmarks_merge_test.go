// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"encoding/xml"
	"strings"
	"testing"

	"gosrc.io/xmpp/stanza"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
)

func TestBookmarkRoomKey(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"room@conference.example.com":       "room@conference.example.com",
		" Room@Conference.Example.COM ":     "room@conference.example.com",
		"room@conference.example.com/alice": "room@conference.example.com",
		"+15550101100@cheogram.com":         "+15550101100@cheogram.com",
		"=2b15550101100@cheogram.com":       "+15550101100@cheogram.com",
		"":                                  "",
	}
	for input, want := range cases {
		if got := bookmarkRoomKey(input); got != want {
			t.Errorf("bookmarkRoomKey(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestMergeBookmark2(t *testing.T) {
	t.Parallel()

	created, changed := mergeBookmark2(nil, BookmarkConference{
		JID:      "room@conference.example.com",
		Name:     "My Room",
		Autojoin: "true",
		Nick:     "alice",
		Password: "secret",
	})
	if !changed {
		t.Fatal("merging into a missing item must count as a change")
	}
	if created.Name != "My Room" || created.Nick != "alice" || created.Password != "secret" || !created.AutojoinEnabled() {
		t.Fatalf("unexpected created bookmark: %+v", created)
	}

	unnamed, _ := mergeBookmark2(nil, BookmarkConference{JID: "room@conference.example.com", Autojoin: "true"})
	if unnamed.Name != "room" {
		t.Fatalf("unnamed bookmark should default to the room localpart, got %q", unnamed.Name)
	}

	existing := &BookmarkConference{
		JID:      "room@conference.example.com",
		Name:     "Their Name",
		Autojoin: "true",
		Nick:     "other",
		Password: "secret",
	}
	merged, changed := mergeBookmark2(existing, BookmarkConference{
		JID:      "room@conference.example.com",
		Name:     "New Name",
		Autojoin: "false",
		Nick:     "alice",
		Password: "new-secret",
	})
	if !changed {
		t.Fatal("autojoin flip must count as a change")
	}
	if merged.Name != "Their Name" {
		t.Fatalf("existing name must be preserved, got %q", merged.Name)
	}
	if merged.Nick != "other" {
		t.Fatalf("existing nick must be preserved, got %q", merged.Nick)
	}
	if merged.Password != "secret" {
		t.Fatalf("existing password must be preserved, got %q", merged.Password)
	}
	if merged.AutojoinEnabled() {
		t.Fatal("autojoin should now be false")
	}

	noop, changed := mergeBookmark2(existing, BookmarkConference{
		JID:      "room@conference.example.com",
		Name:     "Something Else",
		Autojoin: "true",
		Nick:     "alice",
	})
	if changed {
		t.Fatal("nothing changed (autojoin equal, nick non-empty) — publish must be skipped")
	}
	if noop.Nick != "other" {
		t.Fatalf("no-op merge must keep the existing nick, got %q", noop.Nick)
	}

	// A change on an existing item without a name fills the localpart.
	pi, _ := mergeBookmark2(&BookmarkConference{JID: "room@conference.example.com", Autojoin: "true"}, BookmarkConference{
		JID:      "room@conference.example.com",
		Autojoin: "false",
	})
	if pi.Name != "room" {
		t.Fatalf("renamed-on-change bookmark should default to the room localpart, got %q", pi.Name)
	}
}

func TestBookmarkPublishOptionsForm(t *testing.T) {
	t.Parallel()
	form := bookmarkPublishOptionsForm()
	if form == nil || form.Type != "submit" {
		t.Fatalf("unexpected form: %+v", form)
	}
	values := map[string]string{}
	for _, field := range form.Fields {
		if len(field.ValuesList) > 0 {
			values[field.Var] = field.ValuesList[0]
		}
	}
	if values["FORM_TYPE"] != bookmarkPublishOptionsFormType {
		t.Fatalf("missing FORM_TYPE, got %v", values)
	}
	if values["pubsub#access_model"] != "whitelist" {
		t.Fatalf("publish-options must request a whitelist node, got %v", values)
	}
	if values["pubsub#persist_items"] != "true" || values["pubsub#send_last_published_item"] != "never" {
		t.Fatalf("unexpected publish-options values: %v", values)
	}
}

func TestBookmarkErrorDisablesWrites(t *testing.T) {
	t.Parallel()
	cases := []struct {
		reason string
		code   int
		want   bool
	}{
		{"", 0, false},
		{"feature-not-implemented", 0, true},
		{"conflict", 0, true},
		{"Forbidden", 0, true},
		{"not-allowed", 0, true},
		{"bad-request", 0, true},
		{"internal-server-error", 500, false},
		{"", 403, true},
		{"", 409, true},
		{"", 501, true},
		{"", 500, false},
	}
	for _, tc := range cases {
		if got := bookmarkErrorDisablesWrites(tc.reason, tc.code); got != tc.want {
			t.Errorf("bookmarkErrorDisablesWrites(%q, %d) = %v, want %v", tc.reason, tc.code, got, tc.want)
		}
	}

	if bookmarkIQErrorDisablesWrites(nil) {
		t.Fatal("nil IQ must not disable writes")
	}
	if bookmarkIQErrorDisablesWrites(&stanza.IQ{}) {
		t.Fatal("IQ without an error must not disable writes")
	}
	if !bookmarkIQErrorDisablesWrites(&stanza.IQ{Error: &stanza.Err{Reason: "forbidden"}}) {
		t.Fatal("forbidden IQ error must disable writes")
	}
	if !bookmarkPublishDisablesWrites(&bookmarkPublishError{reason: "feature-not-implemented"}) {
		t.Fatal("publish error classification must disable writes")
	}
	if bookmarkPublishDisablesWrites(&bookmarkPublishError{reason: "internal-server-error", code: 500}) {
		t.Fatal("unrelated publish error must not disable writes")
	}
}

func TestParseBookmark2Items(t *testing.T) {
	t.Parallel()
	list := []stanza.Item{
		{
			Id: "room@conference.example.com",
			Any: &stanza.Node{
				XMLName: xml.Name{Space: NSBookmarks2, Local: "conference"},
				Attrs: []xml.Attr{
					{Name: xml.Name{Local: "jid"}, Value: "room@conference.example.com"},
					{Name: xml.Name{Local: "name"}, Value: "My Room"},
					{Name: xml.Name{Local: "autojoin"}, Value: "true"},
				},
				Nodes: []stanza.Node{
					{XMLName: xml.Name{Space: NSBookmarks2, Local: "nick"}, Content: "alice"},
					{XMLName: xml.Name{Space: NSBookmarks2, Local: "password"}, Content: "secret"},
				},
			},
		},
		{
			Id: "no-jid.example.com",
			Any: &stanza.Node{
				XMLName: xml.Name{Space: NSBookmarks2, Local: "conference"},
				Nodes: []stanza.Node{
					{XMLName: xml.Name{Space: NSBookmarks2, Local: "nick"}, Content: "bob"},
				},
			},
		},
		{Id: "foreign", Any: &stanza.Node{XMLName: xml.Name{Space: "storage:bookmarks", Local: "storage"}}},
		{Id: "empty"},
	}
	items := parseBookmark2Items(list)
	if len(items) != 2 {
		t.Fatalf("expected 2 parsed items, got %d: %+v", len(items), items)
	}
	room, ok := items["room@conference.example.com"]
	if !ok {
		t.Fatalf("missing first item: %+v", items)
	}
	if room.Name != "My Room" || room.Nick != "alice" || room.Password != "secret" || !room.AutojoinEnabled() {
		t.Fatalf("unexpected parsed item: %+v", room)
	}
	if _, ok := items["no-jid.example.com"]; !ok {
		t.Fatalf("item without a jid attribute should fall back to the item id: %+v", items)
	}
}

func TestPublishBookmark2MergedWithoutConnection(t *testing.T) {
	t.Parallel()
	nc := &MyNetworkClient{
		login: &bridgev2.UserLogin{UserLogin: &database.UserLogin{Metadata: &LoginMetadata{RemoteUserID: "me@example.com"}}},
	}
	err := nc.publishBookmark2Merged(t.Context(), BookmarkConference{JID: "room@conference.example.com", Autojoin: "true"}, true)
	if err == nil || !strings.Contains(err.Error(), "bookmark fetch before write failed") {
		t.Fatalf("expected a fetch failure without a connection, got %v", err)
	}
	if err := nc.publishBookmark2Merged(t.Context(), BookmarkConference{}, true); err == nil {
		t.Fatal("an empty bookmark JID must fail")
	}
}

func TestBridgedMUCBookmarkCandidatesNilBridge(t *testing.T) {
	t.Parallel()
	nc := &MyNetworkClient{}
	if rooms := nc.bridgedMUCBookmarkCandidates(t.Context()); rooms != nil {
		t.Fatalf("no bridge must yield no candidates, got %v", rooms)
	}
}

func TestRunBookmarkBackfillGuards(t *testing.T) {
	t.Parallel()
	nc := &MyNetworkClient{}
	if created := nc.runBookmarkBackfill(t.Context(), false); created != 0 {
		t.Fatalf("bare client must create nothing, got %d", created)
	}
	done := true
	nc = &MyNetworkClient{
		login: &bridgev2.UserLogin{UserLogin: &database.UserLogin{Metadata: &LoginMetadata{BookmarksBackfilled: done}}},
	}
	if created := nc.runBookmarkBackfill(t.Context(), false); created != 0 {
		t.Fatalf("an already backfilled login must be skipped, got %d", created)
	}
	nc.scheduleBookmarkBackfill() // must not panic or block on a bare client
}
