// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"strings"
	"testing"
	"time"

	"gosrc.io/xmpp/stanza"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

func TestSenderUserID(t *testing.T) {
	t.Parallel()
	nc := &MyNetworkClient{}
	composite := networkid.UserID(EscapeJID("room@conference.example.com") + "/" + EscapeJID("nick"))
	if got := nc.senderUserID("room@conference.example.com/nick"); got != composite {
		t.Fatalf("senderUserID(room/nick) = %q, want %q", got, composite)
	}
	// The label helper resolves the occupant nick so ghost names work.
	if label := contactLabelFromJID(UnescapeJID(string(composite))); label != "nick" {
		t.Fatalf("contactLabelFromJID(occupant) = %q, want %q", label, "nick")
	}
	if got := nc.senderUserID("room@conf/occ-abc"); got != networkid.UserID("room@conf/occ-abc") {
		t.Fatalf("senderUserID(occ) = %q", got)
	}
	if got := nc.senderUserID("alice@example.com"); got != MakeUserID("alice@example.com") {
		t.Fatalf("senderUserID(alice) = %q", got)
	}
	if got := nc.senderUserID("+15550101100@cheogram.com"); got != MakeUserID("+15550101100@cheogram.com") {
		t.Fatalf("senderUserID(cheogram dm) = %q", got)
	}
	if got := nc.senderUserID("+15550101100,+15550101200@cheogram.com/tel"); strings.Contains(string(got), "/") {
		t.Fatalf("senderUserID(cheogram group resource) = %q, want no occupant resource", got)
	}
	if got := nc.senderUserID("   "); got != "" {
		t.Fatalf("senderUserID(blank) = %q, want empty", got)
	}
}

func TestDecodeAvatarData(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		raw    string
		want   string
		wantOK bool
	}{
		"valid":       {`<data xmlns="urn:xmpp:avatar:data">aGVsbG8=</data>`, "hello", true},
		"whitespace":  {`<data xmlns="urn:xmpp:avatar:data">aGVs bG8=</data>`, "hello", true},
		"invalid b64": {`<data xmlns="urn:xmpp:avatar:data">!!!</data>`, "", false},
		"empty":       {`<data xmlns="urn:xmpp:avatar:data"></data>`, "", false},
		"foreign":     {`<metadata xmlns="urn:xmpp:avatar:metadata"/>`, "", false},
	}
	for name, tc := range cases {
		tc := tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, ok := decodeAvatarData([]byte(tc.raw))
			if ok != tc.wantOK {
				t.Fatalf("decodeAvatarData(%s) ok = %v, want %v", tc.raw, ok, tc.wantOK)
			}
			if ok && string(got) != tc.want {
				t.Fatalf("decodeAvatarData(%s) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestFetchOccupantXEP0084AvatarNotConnected(t *testing.T) {
	t.Parallel()
	nc := &MyNetworkClient{}
	if _, _, _, err := nc.fetchOccupantXEP0084Avatar(t.Context(), "room@conference.example.com/nick"); err == nil {
		t.Fatal("expected an error when not connected")
	}
}

func TestAwaitOccupantAvatarSlot(t *testing.T) {
	t.Parallel()
	nc := &MyNetworkClient{}
	if !nc.awaitOccupantAvatarSlot(t.Context()) {
		t.Fatal("first slot should be granted immediately")
	}
	shortCtx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if nc.awaitOccupantAvatarSlot(shortCtx) {
		t.Fatal("second slot should not be granted inside the pacing gap")
	}
}

func TestMaybeFetchOccupantAvatarGuards(t *testing.T) {
	t.Parallel()
	nc := &MyNetworkClient{}
	plain := stanza.Presence{}
	nc.maybeFetchOccupantAvatar("room@conference.example.com", "nick", plain)
	nc.maybeFetchOccupantAvatar("+15550101100,+15550101200@cheogram.com", "+15550101100", plain)
	nc.maybeFetchOccupantAvatar("room@conference.example.com", "occ-abc", plain)
	nc.maybeFetchOccupantAvatar("", "", plain)
	withHash := stanza.Presence{}
	withHash.Extensions = []stanza.PresExtension{XMPPVCardUpdate{Photo: "abc123"}}
	nc.maybeFetchOccupantAvatar("room@conference.example.com", "nick", withHash)
}

func TestOccupantAvatarRoomRefusalBreaker(t *testing.T) {
	t.Parallel()
	nc := &MyNetworkClient{
		occupantAvatarBlocks:   map[string]time.Time{},
		occupantAvatarRoomErrs: map[string]int{},
	}
	room := "room@conference.example.com"
	if nc.occupantAvatarRoomBlocked(room) {
		t.Fatal("room should not be blocked initially")
	}
	for i := 0; i < mucOccupantAvatarRoomErrMax; i++ {
		nc.noteOccupantAvatarRoomError(room)
	}
	if !nc.occupantAvatarRoomBlocked(room) {
		t.Fatal("room should be blocked after repeated permanent failures")
	}
	nc.clearOccupantAvatarRoomError(room)
	if nc.occupantAvatarRoomBlocked(room) {
		t.Fatal("room should be unblocked after a successful fetch")
	}
	nc.occupantAvatarMu.Lock()
	nc.occupantAvatarBlocks[room] = time.Now().Add(-time.Minute)
	nc.occupantAvatarMu.Unlock()
	if nc.occupantAvatarRoomBlocked(room) {
		t.Fatal("expired blocks should clear on read")
	}
	// A single failure on a bare client must be safe (and stay below the log threshold).
	bare := &MyNetworkClient{}
	if bare.occupantAvatarRoomBlocked(room) {
		t.Fatal("bare client should not report blocked rooms")
	}
	bare.noteOccupantAvatarRoomError(room)
	bare.clearOccupantAvatarRoomError(room)
}
