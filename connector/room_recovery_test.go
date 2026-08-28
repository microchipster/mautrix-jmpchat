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

func TestGetChatInfoDMMembers(t *testing.T) {
	nc := &MyNetworkClient{}
	portal := &bridgev2.Portal{
		Portal: &database.Portal{
			PortalKey: networkid.PortalKey{
				ID: MakePortalID("+5550102100@cheogram.com"),
			},
			RoomType: database.RoomTypeDM,
		},
	}

	info, err := nc.GetChatInfo(context.Background(), portal)
	if err != nil {
		t.Fatalf("GetChatInfo failed: %v", err)
	}
	if info == nil {
		t.Fatalf("GetChatInfo returned nil")
	}
	if *info.Type != database.RoomTypeDM {
		t.Errorf("Expected RoomTypeDM, got %v", *info.Type)
	}
	if info.Members == nil {
		t.Fatalf("Expected Members list, got nil")
	}
	if !info.Members.IsFull {
		t.Errorf("Expected IsFull to be true")
	}
	if info.Members.OtherUserID == "" {
		t.Errorf("Expected OtherUserID to be set")
	}

	var hasMe, hasOther bool
	for _, m := range info.Members.Members {
		if m.EventSender.IsFromMe {
			hasMe = true
		} else {
			hasOther = true
		}
	}
	if !hasMe {
		t.Errorf("Expected DM members to contain IsFromMe=true member (Matrix user)")
	}
	if !hasOther {
		t.Errorf("Expected DM members to contain IsFromMe=false member (Ghost)")
	}
}

func TestGetChatInfoCheogramGroupMembers(t *testing.T) {
	nc := &MyNetworkClient{}
	portal := &bridgev2.Portal{
		Portal: &database.Portal{
			PortalKey: networkid.PortalKey{
				ID: MakePortalID("+5550102100,+5550102200@cheogram.com"),
			},
			RoomType: database.RoomTypeDefault,
		},
	}

	info, err := nc.GetChatInfo(context.Background(), portal)
	if err != nil {
		t.Fatalf("GetChatInfo failed: %v", err)
	}
	if info == nil || info.Members == nil {
		t.Fatalf("Expected non-nil members for group chat")
	}
	if !info.Members.IsFull {
		t.Errorf("Expected IsFull true for cheogram group")
	}
	if len(info.Members.Members) != 3 { // me + 2 contacts
		t.Errorf("Expected 3 members, got %d", len(info.Members.Members))
	}
	wantGhosts := map[networkid.UserID]bool{
		MakeUserID("+5550102100@cheogram.com"): false,
		MakeUserID("+5550102200@cheogram.com"): false,
	}
	for _, member := range info.Members.Members {
		if _, ok := wantGhosts[member.Sender]; ok {
			wantGhosts[member.Sender] = true
		}
	}
	for ghostID, found := range wantGhosts {
		if !found {
			t.Errorf("expected distinct group ghost %s", ghostID)
		}
	}
}

func TestGetChatInfoCheogramFormattedGroupMembers(t *testing.T) {
	nc := &MyNetworkClient{}
	portal := &bridgev2.Portal{
		Portal: &database.Portal{
			PortalKey: networkid.PortalKey{
				ID: MakePortalID("(555) 123-4567,5551234568@cheogram.com"),
			},
			RoomType: database.RoomTypeDefault,
		},
	}

	info, err := nc.GetChatInfo(context.Background(), portal)
	if err != nil {
		t.Fatalf("GetChatInfo failed: %v", err)
	}
	if info == nil || info.Members == nil || len(info.Members.Members) != 3 {
		t.Fatalf("expected self plus two formatted phone ghosts, got %#v", info)
	}
	if info.Members.Members[1].Sender == info.Members.Members[2].Sender {
		t.Fatal("formatted group participants collapsed into one ghost")
	}
}
