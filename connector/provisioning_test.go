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

func TestValidateUserID(t *testing.T) {
	t.Parallel()

	c := &MyConnector{}
	tests := map[string]bool{
		"+15550101100":                           true,
		"+15550101100@cheogram.com":              true,
		"=2b15550101100@cheogram.com":            true,
		"5550101100@cheogram.com":                true,
		"user@example.com":                       true,
		"":                                       false,
		"+15550101100,+15550101200@cheogram.com": false,
		"user":                                   false,
		"user name@example.com":                  false,
	}

	for input, want := range tests {
		if got := c.ValidateUserID(networkid.UserID(input)); got != want {
			t.Errorf("ValidateUserID(%q) = %v, want %v", input, got, want)
		}
	}
}

func TestResolveIdentifierInvalid(t *testing.T) {
	t.Parallel()

	c := &MyNetworkClient{}
	for _, input := range []string{"", "   "} {
		if _, err := c.ResolveIdentifier(context.Background(), input, false); err == nil {
			t.Errorf("ResolveIdentifier(%q) should have failed", input)
		}
	}
	if _, err := c.ResolveIdentifier(context.Background(), "+15550101100,+15550101200@cheogram.com", true); err == nil {
		t.Error("ResolveIdentifier for multiple participants should have failed")
	}
}

func TestCreateChatWithGhost(t *testing.T) {
	t.Parallel()

	c := &MyNetworkClient{}
	if _, err := c.CreateChatWithGhost(context.Background(), nil); err == nil {
		t.Error("CreateChatWithGhost(nil) should have failed")
	}
	if _, err := c.CreateChatWithGhost(context.Background(), &bridgev2.Ghost{}); err == nil {
		t.Error("CreateChatWithGhost with an empty ghost should have failed")
	}
	groupGhost := &bridgev2.Ghost{Ghost: &database.Ghost{ID: MakeUserID("+15550101100,+15550101200@cheogram.com")}}
	if _, err := c.CreateChatWithGhost(context.Background(), groupGhost); err == nil {
		t.Error("CreateChatWithGhost for a group JID should have failed")
	}

	resp, err := c.CreateChatWithGhost(context.Background(), &bridgev2.Ghost{Ghost: &database.Ghost{ID: MakeUserID("+15550101100")}})
	if err != nil {
		t.Fatalf("CreateChatWithGhost failed: %v", err)
	}
	if want := MakePortalID("+15550101100"); resp.PortalKey.ID != want {
		t.Errorf("CreateChatWithGhost portal ID = %q, want %q", resp.PortalKey.ID, want)
	}
}

func TestCreateGroupInvalidParams(t *testing.T) {
	t.Parallel()

	c := &MyNetworkClient{}
	if _, err := c.CreateGroup(context.Background(), nil); err == nil {
		t.Error("CreateGroup(nil) should have failed")
	}
	single := &bridgev2.GroupCreateParams{
		Participants: []networkid.UserID{MakeUserID("+15550101100")},
	}
	if _, err := c.CreateGroup(context.Background(), single); err == nil {
		t.Error("CreateGroup with a single participant should have failed")
	}
	nonCheogram := &bridgev2.GroupCreateParams{
		Participants: []networkid.UserID{MakeUserID("+15550101100"), networkid.UserID("alice@example.com")},
	}
	if _, err := c.CreateGroup(context.Background(), nonCheogram); err == nil {
		t.Error("CreateGroup with a non-cheogram participant should have failed")
	}
}
