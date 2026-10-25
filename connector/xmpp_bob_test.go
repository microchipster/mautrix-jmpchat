// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"bytes"
	"testing"

	"gosrc.io/xmpp/stanza"
)

func TestExtractXMPPBob(t *testing.T) {
	t.Parallel()

	valid := decodeTestMessage(t, `<message type="chat" from="alice@example.com">`+
		`<body>cid:sha1-1@bob.xmpp.org</body>`+
		`<data xmlns="urn:xmpp:bob" cid="sha1-1@bob.xmpp.org" type="image/png">aGVsbG8=</data>`+
		`</message>`)
	data, mimeType, cid, ok := extractXMPPBob(valid)
	if !ok {
		t.Fatalf("expected valid BOB data to be extracted")
	}
	if !bytes.Equal(data, []byte("hello")) {
		t.Errorf("decoded data = %q, want %q", data, "hello")
	}
	if mimeType != "image/png" {
		t.Errorf("mimeType = %q, want %q", mimeType, "image/png")
	}
	if cid != "sha1-1@bob.xmpp.org" {
		t.Errorf("cid = %q, want %q", cid, "sha1-1@bob.xmpp.org")
	}

	whitespace := decodeTestMessage(t, `<message type="chat" from="alice@example.com">`+
		`<data xmlns="urn:xmpp:bob" cid="sha1-2@bob.xmpp.org" type="image/png">
			aGVs
			bG8=
		</data>`+
		`</message>`)
	data, _, _, ok = extractXMPPBob(whitespace)
	if !ok || !bytes.Equal(data, []byte("hello")) {
		t.Errorf("whitespace-padded base64 should decode to %q, got ok=%v data=%q", "hello", ok, data)
	}

	noMime := decodeTestMessage(t, `<message type="chat" from="alice@example.com">`+
		`<data xmlns="urn:xmpp:bob" cid="sha1-3@bob.xmpp.org">aGVsbG8=</data>`+
		`</message>`)
	_, mimeType, _, ok = extractXMPPBob(noMime)
	if !ok || mimeType != "application/octet-stream" {
		t.Errorf("missing type should default to octet-stream, got ok=%v mime=%q", ok, mimeType)
	}

	missingCID := decodeTestMessage(t, `<message type="chat" from="alice@example.com">`+
		`<data xmlns="urn:xmpp:bob" type="image/png">aGVsbG8=</data>`+
		`</message>`)
	if _, _, _, ok = extractXMPPBob(missingCID); ok {
		t.Errorf("data without cid must be ignored")
	}

	badBase64 := decodeTestMessage(t, `<message type="chat" from="alice@example.com">`+
		`<data xmlns="urn:xmpp:bob" cid="sha1-4@bob.xmpp.org" type="image/png">!!!not-base64!!!</data>`+
		`</message>`)
	if _, _, _, ok = extractXMPPBob(badBase64); ok {
		t.Errorf("invalid base64 must be ignored")
	}

	plain := decodeTestMessage(t, `<message type="chat" from="alice@example.com"><body>hello</body></message>`)
	if _, _, _, ok = extractXMPPBob(plain); ok {
		t.Errorf("message without BOB data must not extract anything")
	}
}

func TestExtractXMPPBobValueExtension(t *testing.T) {
	t.Parallel()
	msg := stanza.Message{
		Extensions: []stanza.MsgExtension{XMPPBobData{
			CID:  "sha1-5@bob.xmpp.org",
			Type: "image/gif",
			Data: "aGVsbG8=",
		}},
	}
	data, mimeType, _, ok := extractXMPPBob(msg)
	if !ok || string(data) != "hello" || mimeType != "image/gif" {
		t.Errorf("value-type BOB extension not extracted: ok=%v data=%q mime=%q", ok, data, mimeType)
	}
}

func TestBobInlineFileName(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"image/png":     "inline.png",
		"image/jpeg":    "inline.jpg",
		"image/gif":     "inline.gif",
		"image/webp":    "inline.webp",
		"application/x": "inline.bin",
		"IMAGE/PNG":     "inline.png",
		"":              "inline.bin",
	}
	for input, want := range tests {
		if got := bobInlineFileName(input); got != want {
			t.Errorf("bobInlineFileName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestCidFallbackBody(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		body   string
		want   string
		wantOK bool
	}{
		{name: "plain text unchanged", body: "hello", want: "hello", wantOK: false},
		{name: "empty unchanged", body: "", want: "", wantOK: false},
		{name: "bare cid reference", body: "cid:abc123@bob.xmpp.org", want: "📎 Inline image is not available (abc123@bob.xmpp.org)", wantOK: true},
		{name: "uppercase scheme", body: "CID:ref@example.com", want: "📎 Inline image is not available (ref@example.com)", wantOK: true},
		{name: "padded cid", body: "  cid:ref  ", want: "📎 Inline image is not available (ref)", wantOK: true},
		{name: "empty reference", body: "cid:", want: "📎 Inline image is not available", wantOK: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := cidFallbackBody(tc.body)
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("cidFallbackBody(%q) = (%q, %v), want (%q, %v)", tc.body, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}
