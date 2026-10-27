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
	if room.ItemID != "room@conference.example.com" {
		t.Fatalf("the pubsub item ID must be preserved for retraction, got %q", room.ItemID)
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

func TestBookmarkRoomLooksLikeKnownMUC(t *testing.T) {
	t.Parallel()

	nc := &MyNetworkClient{
		mucRoomChecks: map[string]mucRoomCheckEntry{
			"known@example.com":   {isRoom: true},
			"unknown@example.com": {isRoom: false},
		},
	}
	tests := []struct {
		name string
		jid  string
		want bool
	}{
		{name: "cached positive", jid: "known@example.com", want: true},
		{name: "conference subdomain", jid: "room@conference.example.org", want: true},
		{name: "groups subdomain", jid: "room@groups.example.com", want: true},
		{name: "muc subdomain", jid: "room@muc.example.org", want: true},
		{name: "cached negative", jid: "unknown@example.com", want: false},
		{name: "account JID", jid: "alice@example.com", want: false},
		{name: "empty", jid: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := nc.bookmarkRoomLooksLikeKnownMUC(tt.jid); got != tt.want {
				t.Errorf("bookmarkRoomLooksLikeKnownMUC(%q) = %t, want %t", tt.jid, got, tt.want)
			}
		})
	}
}

func TestBookmarkRetractionItemID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		existing BookmarkConference
		roomKey  string
		want     string
	}{
		{
			name:     "fetched item ID wins",
			existing: BookmarkConference{JID: "room@conference.example.org", ItemID: "custom-item-id"},
			roomKey:  "room@conference.example.org",
			want:     "custom-item-id",
		},
		{
			name:     "room JID fallback",
			existing: BookmarkConference{JID: "room@conference.example.org"},
			roomKey:  "room@conference.example.org",
			want:     "room@conference.example.org",
		},
		{
			name:     "canonical key fallback",
			existing: BookmarkConference{},
			roomKey:  "room@conference.example.org",
			want:     "room@conference.example.org",
		},
		{
			name:     "blank item ID is ignored",
			existing: BookmarkConference{JID: "room@conference.example.org", ItemID: "  "},
			roomKey:  "room@conference.example.org",
			want:     "room@conference.example.org",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := bookmarkRetractionItemID(tt.existing, tt.roomKey); got != tt.want {
				t.Errorf("bookmarkRetractionItemID(%+v, %q) = %q, want %q", tt.existing, tt.roomKey, got, tt.want)
			}
		})
	}
}

func TestHandleBookmarkConferencesSkipsNonMUCJIDs(t *testing.T) {
	t.Parallel()

	nc := &MyNetworkClient{
		login: &bridgev2.UserLogin{UserLogin: &database.UserLogin{
			Metadata: &LoginMetadata{RemoteUserID: "alice@example.com"},
		}},
	}
	nc.handleBookmarkConferences([]BookmarkConference{
		{JID: "alice@example.com", Autojoin: "true"},
		{JID: "+15550101100@cheogram.com", Autojoin: "true"},
	}, "test")
	if nc.bookmarkedRooms != nil {
		t.Fatalf("non-MUC JIDs must not be recorded as bookmarks: %v", nc.bookmarkedRooms)
	}
	if nc.mucRooms != nil {
		t.Fatalf("non-MUC JIDs must not be joined: %v", nc.mucRooms)
	}
	if nc.mucHealth != nil {
		t.Fatalf("non-MUC JIDs must not enter the join watchdog state: %v", nc.mucHealth)
	}
}

func TestRemoveBookmarkGuards(t *testing.T) {
	t.Parallel()

	nc := &MyNetworkClient{}
	if _, err := nc.removeBookmark(t.Context(), ""); err == nil {
		t.Fatal("an empty JID must be rejected")
	}
	if _, err := nc.removeBookmark(t.Context(), "not-a-room-jid"); err == nil {
		t.Fatal("a JID without a domain must be rejected")
	}

	// Without a connection the retraction fails and the local state must be
	// left untouched, so a retried command sees the bookmark again.
	nc = &MyNetworkClient{
		login:           &bridgev2.UserLogin{UserLogin: &database.UserLogin{Metadata: &LoginMetadata{RemoteUserID: "me@example.com"}}},
		bookmarkedRooms: map[string]bool{"room@conference.example.org": true},
	}
	if _, err := nc.removeBookmark(t.Context(), "room@conference.example.org"); err == nil || !strings.Contains(err.Error(), "fetch") {
		t.Fatalf("expected a retraction fetch failure without a connection, got %v", err)
	}
	if !nc.bookmarkedRooms["room@conference.example.org"] {
		t.Fatal("a failed retraction must not drop the local bookmark state")
	}
}

func TestLogNonMUCBookmarkOnce(t *testing.T) {
	t.Parallel()

	nc := &MyNetworkClient{}
	nc.logNonMUCBookmarkOnce("alice@example.com", "test")
	nc.logNonMUCBookmarkOnce("alice@example.com", "test") // must not panic or log again
	if !nc.nonMUCBookmarkLogged["alice@example.com"] {
		t.Fatal("the JID must be remembered as already reported")
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
