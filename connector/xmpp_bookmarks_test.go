// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"reflect"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
)

func TestBookmarkSnapshot(t *testing.T) {
	t.Parallel()
	nc := &MyNetworkClient{}
	if rooms := nc.bookmarkSnapshot(); rooms != nil {
		t.Errorf("nil bookmark map should yield a nil snapshot, got %v", rooms)
	}
	nc.bookmarkedRooms = map[string]bool{
		" room-b@conference.example.com ": true,
		"room-a@conference.example.com":   true,
		"":                                true,
		"   ":                             true,
	}
	want := []string{"room-a@conference.example.com", "room-b@conference.example.com"}
	if rooms := nc.bookmarkSnapshot(); !reflect.DeepEqual(rooms, want) {
		t.Errorf("unexpected snapshot: %v, want %v", rooms, want)
	}
}

func TestPushBookmarksWithoutConnection(t *testing.T) {
	t.Parallel()
	nc := &MyNetworkClient{
		login: &bridgev2.UserLogin{UserLogin: &database.UserLogin{Metadata: &LoginMetadata{}}},
		bookmarkedRooms: map[string]bool{
			"room-a@conference.example.com": true,
		},
	}
	if pushed := nc.pushBookmarks(t.Context()); pushed != 0 {
		t.Errorf("pushBookmarks without a connection should push nothing, got %d", pushed)
	}
}
