// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
)

func TestTrustedStanzaID(t *testing.T) {
	t.Parallel()
	withStanzaID := func(id, by string) string {
		byAttr := ""
		if by != "" {
			byAttr = ` by="` + by + `"`
		}
		return `<message from="alice@example.com" type="chat" id="raw-1">` +
			`<stanza-id xmlns="urn:xmpp:stanza-id:0" id="` + id + `"` + byAttr + `/>` +
			`</message>`
	}

	tests := []struct {
		name        string
		raw         string
		bareFromJid string
		isFromMe    bool
		want        string
	}{
		{
			name:        "matching sender bare jid accepted",
			raw:         withStanzaID("sid-1", "alice@example.com"),
			bareFromJid: "alice@example.com",
			want:        "sid-1",
		},
		{
			name:        "sender domain accepted",
			raw:         withStanzaID("sid-2", "example.com"),
			bareFromJid: "alice@example.com",
			want:        "sid-2",
		},
		{
			name:        "empty by accepted",
			raw:         withStanzaID("sid-3", ""),
			bareFromJid: "alice@example.com",
			want:        "sid-3",
		},
		{
			name:        "mismatched by rejected",
			raw:         withStanzaID("sid-4", "mallory@evil.example"),
			bareFromJid: "alice@example.com",
			want:        "",
		},
		{
			name:        "mismatched domain rejected",
			raw:         withStanzaID("sid-5", "evil.example"),
			bareFromJid: "alice@example.com",
			want:        "",
		},
		{
			name:        "no stanza-id",
			raw:         `<message from="alice@example.com" type="chat" id="raw-1"></message>`,
			bareFromJid: "alice@example.com",
			want:        "",
		},
		{
			name:        "case-insensitive match",
			raw:         withStanzaID("sid-6", "Alice@Example.COM"),
			bareFromJid: "alice@example.com",
			want:        "sid-6",
		},
	}

	nc := &MyNetworkClient{}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			msg := decodeTestMessage(t, tc.raw)
			got := nc.trustedStanzaID(msg, tc.bareFromJid, tc.isFromMe)
			if got != tc.want {
				t.Errorf("trustedStanzaID() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTrustedStanzaIDFromMe(t *testing.T) {
	t.Parallel()
	nc := &MyNetworkClient{login: &bridgev2.UserLogin{UserLogin: &database.UserLogin{Metadata: &LoginMetadata{RemoteUserID: "me@example.com"}}}}

	own := decodeTestMessage(t, `<message from="me@example.com" type="chat" id="raw-1">`+
		`<stanza-id xmlns="urn:xmpp:stanza-id:0" id="sid-own" by="me@example.com"/>`+
		`</message>`)
	if got := nc.trustedStanzaID(own, "me@example.com", true); got != "sid-own" {
		t.Errorf("trustedStanzaID() own JID = %q, want %q", got, "sid-own")
	}

	foreign := decodeTestMessage(t, `<message from="me@example.com" type="chat" id="raw-2">`+
		`<stanza-id xmlns="urn:xmpp:stanza-id:0" id="sid-foreign" by="alice@example.com"/>`+
		`</message>`)
	if got := nc.trustedStanzaID(foreign, "alice@example.com", true); got != "" {
		t.Errorf("trustedStanzaID() foreign by = %q, want empty", got)
	}
}

func TestExtractMessageIDTrust(t *testing.T) {
	t.Parallel()
	nc := &MyNetworkClient{}

	tests := []struct {
		name        string
		raw         string
		bareFromJid string
		isFromMe    bool
		wantID      string
		wantServer  string
	}{
		{
			name: "origin id preferred over stanza id",
			raw: `<message from="alice@example.com" type="chat" id="raw-1">` +
				`<origin-id xmlns="urn:xmpp:origin-id:0" id="o-1"/>` +
				`<stanza-id xmlns="urn:xmpp:stanza-id:0" id="sid-1" by="alice@example.com"/>` +
				`</message>`,
			bareFromJid: "alice@example.com",
			wantID:      "o-1",
			wantServer:  "sid-1",
		},
		{
			name: "trusted stanza id used when origin missing",
			raw: `<message from="alice@example.com" type="chat" id="raw-1">` +
				`<stanza-id xmlns="urn:xmpp:stanza-id:0" id="sid-2" by="example.com"/>` +
				`</message>`,
			bareFromJid: "alice@example.com",
			wantID:      "sid-2",
			wantServer:  "sid-2",
		},
		{
			name: "untrusted stanza id falls back to raw id",
			raw: `<message from="alice@example.com" type="chat" id="raw-3">` +
				`<stanza-id xmlns="urn:xmpp:stanza-id:0" id="sid-3" by="mallory@evil.example"/>` +
				`</message>`,
			bareFromJid: "alice@example.com",
			wantID:      "raw-3",
			wantServer:  "",
		},
		{
			name:        "raw id fallback",
			raw:         `<message from="alice@example.com" type="chat" id="raw-4"></message>`,
			bareFromJid: "alice@example.com",
			wantID:      "raw-4",
			wantServer:  "",
		},
		{
			name:        "no ids at all",
			raw:         `<message from="alice@example.com" type="chat"></message>`,
			bareFromJid: "alice@example.com",
			wantID:      "",
			wantServer:  "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			msg := decodeTestMessage(t, tc.raw)
			if got := nc.extractMessageID(msg, tc.bareFromJid, tc.isFromMe); got != tc.wantID {
				t.Errorf("extractMessageID() = %q, want %q", got, tc.wantID)
			}
			if got := nc.extractServerMessageID(msg, tc.bareFromJid, tc.isFromMe); got != tc.wantServer {
				t.Errorf("extractServerMessageID() = %q, want %q", got, tc.wantServer)
			}
		})
	}
}
