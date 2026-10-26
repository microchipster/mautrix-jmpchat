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
	"maunium.net/go/mautrix/id"
)

func TestRewriteMentionsUnchangedWithoutHTML(t *testing.T) {
	t.Parallel()
	resolve := func(id.UserID) (string, bool) { return "Alice", true }
	if got := rewriteMentions("hello", "", true, resolve); got != "hello" {
		t.Fatalf("body without formatted body changed: %q", got)
	}
	if got := rewriteMentions("hello", "<b>Alice (jmp)</b>", false, resolve); got != "hello" {
		t.Fatalf("body with non-HTML formatting changed: %q", got)
	}
}

func TestRewriteMentionsReplacesDisplaynameInBody(t *testing.T) {
	t.Parallel()
	resolve := func(mxid id.UserID) (string, bool) {
		if mxid == "@ghost:example.com" {
			return "Alice", true
		}
		return "", false
	}
	body := "ping Alice (jmp) please"
	formatted := `ping <a href="https://matrix.to/#/@ghost:example.com">Alice (jmp)</a> please`
	want := "ping Alice please"
	if got := rewriteMentions(body, formatted, true, resolve); got != want {
		t.Fatalf("displayname rewrite mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestRewriteMentionsReplacesRawMXIDInBody(t *testing.T) {
	t.Parallel()
	resolve := func(mxid id.UserID) (string, bool) {
		if mxid == "@ghost:example.com" {
			return "Alice", true
		}
		return "", false
	}
	body := "ping @ghost:example.com please"
	formatted := `ping <a href="https://matrix.to/#/@ghost:example.com">Alice (jmp)</a> please`
	want := "ping Alice please"
	if got := rewriteMentions(body, formatted, true, resolve); got != want {
		t.Fatalf("raw mxid rewrite mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestRewriteMentionsUnresolvedKeepsText(t *testing.T) {
	t.Parallel()
	resolve := func(id.UserID) (string, bool) { return "", false }
	body := "ping Alice (jmp) please"
	formatted := `ping <a href="https://matrix.to/#/@ghost:example.com">Alice (jmp)</a> please`
	if got := rewriteMentions(body, formatted, true, resolve); got != body {
		t.Fatalf("unresolved mention changed the body: %q", got)
	}
}

func TestRewriteMentionsHTMLEventDerivesPlainBody(t *testing.T) {
	t.Parallel()
	resolve := func(mxid id.UserID) (string, bool) {
		if mxid == "@ghost:example.com" {
			return "Alice", true
		}
		return "", false
	}
	formatted := `hi <a href="https://matrix.to/#/@ghost:example.com">Alice (jmp)</a>!`
	want := "hi Alice!"
	if got := rewriteMentions("", formatted, true, resolve); got != want {
		t.Fatalf("HTML-only derive mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestRewriteMentionsDocumentOrder(t *testing.T) {
	t.Parallel()
	resolve := func(mxid id.UserID) (string, bool) {
		switch mxid {
		case "@first:example.com":
			return "First", true
		case "@second:example.com":
			return "Second", true
		}
		return "", false
	}
	body := "Sam and Sam"
	formatted := `<a href="https://matrix.to/#/@first:example.com">Sam</a> and <a href="https://matrix.to/#/@second:example.com">Sam</a>`
	want := "First and Second"
	if got := rewriteMentions(body, formatted, true, resolve); got != want {
		t.Fatalf("document-order rewrite mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestXMPPNameForGhost(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bare := &MyNetworkClient{}
	// Ghost IDs normally have no resource (MakeUserID canonicalizes), but a raw network ID with an
	// occupant resource still resolves to the nick.
	if got := bare.xmppNameForGhost(ctx, networkid.UserID("room@conference.example.com/nick")); got != "nick" {
		t.Fatalf("occupant ghost name = %q, want %q", got, "nick")
	}
	cases := map[string]string{
		"+15551234567@cheogram.com": "+15551234567",
		"alice@example.com":         "alice",
	}
	for ghostID, want := range cases {
		if got := bare.xmppNameForGhost(ctx, MakeUserID(ghostID)); got != want {
			t.Fatalf("xmppNameForGhost(%q) = %q, want %q", ghostID, got, want)
		}
	}
	own := &MyNetworkClient{login: &bridgev2.UserLogin{UserLogin: &database.UserLogin{ID: "me@example.com"}}}
	if got := own.xmppNameForGhost(ctx, MakeUserID("me@example.com")); got != "me" {
		t.Fatalf("own login name = %q, want %q", got, "me")
	}
}

func TestMentionResolverNilBridge(t *testing.T) {
	t.Parallel()
	resolve := (&MyNetworkClient{}).mentionResolver(context.Background(), nil)
	if name, ok := resolve("@ghost:example.com"); ok || name != "" {
		t.Fatalf("nil bridge resolver returned (%q, %v), want empty", name, ok)
	}
}
