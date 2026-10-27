// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"testing"

	"gosrc.io/xmpp/stanza"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

func TestIsCheogramGatewayJID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		jid  string
		want bool
	}{
		{name: "bare gateway JID", jid: "cheogram.com", want: true},
		{name: "bare gateway JID uppercase", jid: "CHEOGRAM.COM", want: true},
		{name: "bare gateway JID with resource", jid: "cheogram.com/resource", want: true},
		{name: "single contact", jid: "+15551234567@cheogram.com", want: true},
		{name: "single contact escaped", jid: "=2b15551234567@cheogram.com", want: true},
		{name: "short code", jid: "1234@cheogram.com", want: true},
		{name: "non-phone localpart", jid: "alice@cheogram.com", want: true},
		{name: "group text", jid: "+15550100100,+15550100200@cheogram.com", want: true},
		{name: "group text escaped", jid: "=2b15550100100,=2b15550100200@cheogram.com", want: true},
		{name: "group text with resource", jid: "+15550100100,+15550100200@cheogram.com/tel", want: true},
		{name: "group text uppercase domain", jid: "+15550100100,+15550100200@Cheogram.com", want: true},
		{name: "real MUC", jid: "room@conference.example.org", want: false},
		{name: "subdomain is not the gateway", jid: "room@conference.cheogram.com", want: false},
		{name: "comma group on other domain", jid: "+15551234567,+15557654321@example.org", want: false},
		{name: "suffix spoof", jid: "user@notcheogram.com", want: false},
		{name: "suffix spoof with dot", jid: "user@cheogram.com.evil", want: false},
		{name: "other domain", jid: "user@example.com", want: false},
		{name: "empty", jid: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isCheogramGatewayJID(tt.jid); got != tt.want {
				t.Errorf("isCheogramGatewayJID(%q) = %t, want %t", tt.jid, got, tt.want)
			}
		})
	}
}

func TestJoinMUCSkipsCheogramGatewayJID(t *testing.T) {
	t.Parallel()

	nc := &MyNetworkClient{}
	nc.joinMUC("+15550100100,+15550100200@cheogram.com", "", "")
	if nc.mucNick != nil || nc.mucRooms != nil || nc.mucPasswords != nil {
		t.Fatal("joinMUC must not touch MUC state for a Cheogram gateway JID")
	}
}

func TestIsNonMUCRoomJID(t *testing.T) {
	t.Parallel()

	nc := &MyNetworkClient{
		login: &bridgev2.UserLogin{UserLogin: &database.UserLogin{
			Metadata: &LoginMetadata{RemoteUserID: "alice@example.com"},
		}},
	}
	tests := []struct {
		name string
		jid  string
		want bool
	}{
		{name: "own bare JID", jid: "alice@example.com", want: true},
		{name: "own JID with resource", jid: "alice@example.com/phone", want: true},
		{name: "own JID mixed case", jid: "Alice@Example.Com", want: true},
		{name: "cheogram contact", jid: "+15550101100@cheogram.com", want: true},
		{name: "real MUC", jid: "room@groups.example.com", want: false},
		{name: "other account JID", jid: "bob@example.com", want: false},
		{name: "other domain MUC", jid: "room@conference.example.org", want: false},
		{name: "empty", jid: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := nc.isNonMUCRoomJID(tt.jid); got != tt.want {
				t.Errorf("isNonMUCRoomJID(%q) = %t, want %t", tt.jid, got, tt.want)
			}
		})
	}

	if noLogin := (&MyNetworkClient{}); noLogin.isNonMUCRoomJID("alice@example.com") {
		t.Error("without a login there is no own JID to match")
	}
}

func TestInitiateGroupChatSkipsMUCSetupForCheogram(t *testing.T) {
	t.Parallel()

	nc := &MyNetworkClient{}
	roomJID, name, err := nc.initiateGroupChat(context.Background(), "Family", []string{"+15550100100", "+15550100200"})
	if err != nil {
		t.Fatalf("initiateGroupChat failed: %v", err)
	}
	if roomJID != "+15550100100,+15550100200@cheogram.com" {
		t.Fatalf("roomJID = %q, want %q", roomJID, "+15550100100,+15550100200@cheogram.com")
	}
	if name != "Family" {
		t.Fatalf("name = %q, want %q", name, "Family")
	}
	if len(nc.mucPendingCreate) != 0 {
		t.Fatal("initiateGroupChat must not queue a MUC creation for Cheogram group JIDs")
	}
	if nc.bookmarkedRooms != nil || nc.bookmarkedRoomNames != nil {
		t.Fatal("initiateGroupChat must not record a bookmark for Cheogram group JIDs")
	}
}

func TestOutboundXMPPStanzaType(t *testing.T) {
	t.Parallel()

	makePortal := func(jid string, isMUC bool) *bridgev2.Portal {
		var meta *PortalMetadata
		if isMUC {
			meta = &PortalMetadata{IsMUC: true}
		}
		return &bridgev2.Portal{
			Portal: &database.Portal{
				PortalKey: networkid.PortalKey{ID: MakePortalID(jid)},
				Metadata:  meta,
			},
		}
	}

	tests := []struct {
		name   string
		portal *bridgev2.Portal
		want   stanza.StanzaType
	}{
		{name: "nil portal", portal: nil, want: stanza.MessageTypeChat},
		{name: "cheogram DM", portal: makePortal("+15551234567@cheogram.com", false), want: stanza.MessageTypeChat},
		{name: "cheogram group text", portal: makePortal("+15550100100,+15550100200@cheogram.com", false), want: stanza.MessageTypeChat},
		{name: "cheogram group text marked MUC", portal: makePortal("+15550100100,+15550100200@cheogram.com", true), want: stanza.MessageTypeChat},
		{name: "bare cheogram gateway", portal: makePortal("cheogram.com", false), want: stanza.MessageTypeChat},
		{name: "real MUC", portal: makePortal("room@conference.example.org", true), want: stanza.MessageTypeGroupchat},
		{name: "plain room without metadata", portal: makePortal("room@conference.example.org", false), want: stanza.MessageTypeChat},
		{name: "plain non-cheogram DM", portal: makePortal("+15551234567@example.org", false), want: stanza.MessageTypeChat},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := outboundXMPPStanzaType(tt.portal); got != tt.want {
				t.Errorf("outboundXMPPStanzaType(%v) = %q, want %q", tt.portal, got, tt.want)
			}
		})
	}
}
