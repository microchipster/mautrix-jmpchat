// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

func TestShouldSkipGhostNameUpdate(t *testing.T) {
	tests := []struct {
		name                     string
		ghostName, desired, live string
		want                     bool
	}{
		{name: "same name no-op", ghostName: "+1@example.com", desired: "+1@example.com", live: "+1@example.com", want: false},
		{name: "foreign live name on fresh ghost skips", ghostName: "", desired: "+1@example.com", live: "Some Person", want: true},
		{name: "actively re-proposed name with foreign live name skips", ghostName: "+1@example.com", desired: "+2@example.com", live: "Some Person", want: true},
		{name: "no live name allows bridge write", ghostName: "", desired: "+1@example.com", live: "", want: false},
		{name: "live matches stored allows bridge write", ghostName: "+1@example.com", desired: "+2@example.com", live: "+1@example.com", want: false},
		{name: "live already equals desired skips redundant write", ghostName: "Old", desired: "New", live: "New", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldSkipGhostNameUpdate(tt.ghostName, tt.desired, tt.live); got != tt.want {
				t.Errorf("shouldSkipGhostNameUpdate(%q, %q, %q) = %v, want %v", tt.ghostName, tt.desired, tt.live, got, tt.want)
			}
		})
	}
}

func TestDesiredGhostName(t *testing.T) {
	ghostFromID := func(id string, meta *GhostMetadata) *bridgev2.Ghost {
		return &bridgev2.Ghost{
			Ghost: &database.Ghost{
				ID:       networkid.UserID(id),
				Metadata: meta,
			},
		}
	}

	t.Run("bare jid fallback", func(t *testing.T) {
		nc := &MyNetworkClient{}
		desired, custom := nc.desiredGhostName(ghostFromID("+1@example.com", nil))
		if desired != "+1@example.com" {
			t.Errorf("desired = %q, want %q", desired, "+1@example.com")
		}
		if custom != "" {
			t.Errorf("custom = %q, want empty", custom)
		}
	})

	t.Run("resource part preferred when no custom", func(t *testing.T) {
		nc := &MyNetworkClient{}
		desired, _ := nc.desiredGhostName(ghostFromID("+1@example.com/Typing", &GhostMetadata{}))
		if desired != "Typing" {
			t.Errorf("desired = %q, want %q", desired, "Typing")
		}
	})

	t.Run("custom name wins", func(t *testing.T) {
		nc := &MyNetworkClient{}
		desired, custom := nc.desiredGhostName(ghostFromID("+1@example.com", &GhostMetadata{CustomName: "Some Person"}))
		if desired != "Some Person" || custom != "Some Person" {
			t.Errorf("desired = %q custom = %q, want %q/%q", desired, custom, "Some Person", "Some Person")
		}
	})
}

func TestManualNameCache(t *testing.T) {
	nc := &MyNetworkClient{}
	userID := networkid.UserID("=2b12345678901@cheogram.com")
	if nc.isManualName(userID) {
		t.Fatal("expected not manually named initially")
	}
	nc.markManualName(userID)
	if !nc.isManualName(userID) {
		t.Fatal("expected manually named after mark")
	}
}

func TestGetUserInfoEmptyGhost(t *testing.T) {
	nc := &MyNetworkClient{}
	info, err := nc.GetUserInfo(context.Background(), &bridgev2.Ghost{
		Ghost: &database.Ghost{ID: ""},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Name != nil {
		t.Errorf("expected no name for empty ghost id, got %q", *info.Name)
	}
}
