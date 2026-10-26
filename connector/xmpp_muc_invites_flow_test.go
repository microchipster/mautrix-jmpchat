// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"errors"
	"strings"
	"testing"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"
)

func TestIsCheogramContactJID(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"+15550101100@cheogram.com":              true,
		"15550101100@cheogram.com":               true,
		"1234@cheogram.com":                      true, // short code
		"+15550101100,+15550101200@cheogram.com": true, // group MMS
		"alice@cheogram.com":                     false,
		"room@conference.example.com":            false,
		"user@example.com":                       false,
		"":                                       false,
		"+15550101100":                           true, // canonicalized to a Cheogram number
	}
	for jid, want := range cases {
		if got := isCheogramContactJID(jid); got != want {
			t.Errorf("isCheogramContactJID(%q) = %v, want %v", jid, got, want)
		}
	}
}

func TestMucDomainHeuristic(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"room@conference.example.com":  true,
		"room@muc.example.com":         true,
		"room@groups.example.com":      true,
		"room@example.com":             false,
		"alice@conference.example.com": false, // no domain match? domain is conference.example.com -> true actually
		"":                             false,
	}
	// Correct the intentionally ambiguous case: a bare JID on the conference
	// subdomain is still a heuristic room.
	cases["alice@conference.example.com"] = true
	for jid, want := range cases {
		if got := mucDomainHeuristic(jid); got != want {
			t.Errorf("mucDomainHeuristic(%q) = %v, want %v", jid, got, want)
		}
	}
}

func TestSanitizeInviteReason(t *testing.T) {
	t.Parallel()
	if got := sanitizeInviteReason("plain reason"); got != "plain reason" {
		t.Fatalf("plain reason changed: %q", got)
	}
	if got := sanitizeInviteReason("*bold* _it_"); !strings.Contains(got, `\*bold\*`) {
		t.Fatalf("markdown not escaped: %q", got)
	}
	if got := sanitizeInviteReason("line\nbreak\ttab"); got != "line break tab" {
		t.Fatalf("whitespace not flattened: %q", got)
	}
	if got := sanitizeInviteReason("a\x00b"); got != "ab" {
		t.Fatalf("control characters not dropped: %q", got)
	}
	long := sanitizeInviteReason(strings.Repeat("x", 400))
	if len(long) > 310 || !strings.HasSuffix(long, "...") {
		t.Fatalf("long reason not capped: len=%d", len(long))
	}
}

func TestInviteText(t *testing.T) {
	t.Parallel()
	inv := mucInvite{room: "escape@conference.example.com", inviter: "alice@example.com", reason: "come *here*", password: "s3cret"}
	text := inviteText(inv, "Accept the invite to join.")
	for _, want := range []string{"`alice@example.com`", "`escape@conference.example.com`", `come \*here\*`, "Accept the invite to join.", "password"} {
		if !strings.Contains(text, want) {
			t.Errorf("inviteText missing %q: %q", want, text)
		}
	}
	// An unknown inviter and no reason/password.
	text = inviteText(mucInvite{room: "room@conf.example.com"}, "tail")
	if !strings.Contains(text, "Someone invited you to") || strings.Contains(text, "password") {
		t.Errorf("unexpected inviteText: %q", text)
	}
}

func TestMarkInvitePending(t *testing.T) {
	t.Parallel()
	meta := &PortalMetadata{}
	if !markInvitePending(meta) {
		t.Fatal("first mark should report a change")
	}
	if !meta.MUCLeft || !meta.MUCInvitePending {
		t.Fatal("both flags should be set")
	}
	if markInvitePending(meta) {
		t.Fatal("second mark should not report a change")
	}
}

func TestIsMUCRoomCache(t *testing.T) {
	t.Parallel()
	now := time.Now()
	nc := &MyNetworkClient{
		mucRoomChecks: map[string]mucRoomCheckEntry{
			"cached-room@example.com":               {isRoom: true, at: now},
			"fresh-negative@example.com":            {isRoom: false, at: now},
			"stale-negative@conference.example.com": {isRoom: false, at: now.Add(-2 * mucRoomCheckNegativeTTL)},
		},
	}
	if !nc.isMUCRoom(t.Context(), "cached-room@example.com") {
		t.Fatal("a cached positive result should be reused")
	}
	if nc.isMUCRoom(t.Context(), "fresh-negative@example.com") {
		t.Fatal("a fresh negative result should be reused")
	}
	// The stale negative entry is re-probed; the client is not connected, so
	// the domain heuristic decides (conference. domain -> room).
	if !nc.isMUCRoom(t.Context(), "stale-negative@conference.example.com") {
		t.Fatal("a stale negative result should be re-probed and the heuristic applied")
	}
	if nc.isMUCRoom(t.Context(), "") {
		t.Fatal("an empty JID is not a room")
	}
}

func TestClearMUCInviteStateNilBridge(t *testing.T) {
	t.Parallel()
	nc := &MyNetworkClient{}
	nc.clearMUCInviteState(t.Context(), "room@conf.example.com") // must not panic
}

func TestHandleMatrixMembershipGuards(t *testing.T) {
	t.Parallel()
	login := &bridgev2.UserLogin{UserLogin: &database.UserLogin{ID: "login1"}}
	nc := &MyNetworkClient{login: login}

	if _, err := nc.HandleMatrixMembership(t.Context(), nil); !errors.Is(err, bridgev2.ErrMembershipNotSupported) {
		t.Fatalf("nil change should be unsupported: %v", err)
	}
	if _, err := nc.HandleMatrixMembership(t.Context(), &bridgev2.MatrixMembershipChange{Type: bridgev2.Leave}); !errors.Is(err, bridgev2.ErrMembershipNotSupported) {
		t.Fatalf("Leave should be unsupported: %v", err)
	}

	// An accept targeting another login is refused.
	other := &bridgev2.UserLogin{UserLogin: &database.UserLogin{ID: "login2"}}
	_, err := nc.HandleMatrixMembership(t.Context(), &bridgev2.MatrixMembershipChange{
		Type:   bridgev2.AcceptInvite,
		Target: other,
	})
	if !errors.Is(err, bridgev2.ErrMembershipNotSupported) {
		t.Fatalf("foreign accept should be unsupported: %v", err)
	}

	// An accept of a portal that is not invite-pending is a silent no-op.
	portal := &bridgev2.Portal{Portal: &database.Portal{
		ID:       MakePortalID("room@conference.example.com"),
		Metadata: &PortalMetadata{},
	}}
	res, err := nc.HandleMatrixMembership(t.Context(), &bridgev2.MatrixMembershipChange{
		Type:   bridgev2.AcceptInvite,
		Target: login,
		MatrixRoomMeta: bridgev2.MatrixRoomMeta[*event.MemberEventContent]{
			MatrixEventBase: bridgev2.MatrixEventBase[*event.MemberEventContent]{
				Portal:  portal,
				Content: &event.MemberEventContent{},
			},
		},
	})
	if err != nil || res != nil {
		t.Fatalf("non-pending accept should be a no-op: res=%v err=%v", res, err)
	}
}
