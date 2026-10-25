// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"reflect"
	"testing"

	"gosrc.io/xmpp/stanza"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func TestXMPPMentionJIDs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{
			name: "single mention",
			raw: `<message from="alice@example.com" type="groupchat">` +
				`<reference xmlns="urn:xmpp:reference:0" type="mention" uri="xmpp:+15550101100@cheogram.com"/>` +
				`</message>`,
			want: []string{"+15550101100@cheogram.com"},
		},
		{
			name: "multiple mentions",
			raw: `<message from="alice@example.com" type="groupchat">` +
				`<reference xmlns="urn:xmpp:reference:0" type="mention" uri="xmpp:+15550101100@cheogram.com"/>` +
				`<reference xmlns="urn:xmpp:reference:0" type="mention" uri="xmpp:alice@example.com"/>` +
				`</message>`,
			want: []string{"+15550101100@cheogram.com", "alice@example.com"},
		},
		{
			name: "mention without type attribute",
			raw: `<message from="alice@example.com" type="chat">` +
				`<reference xmlns="urn:xmpp:reference:0" uri="xmpp:alice@example.com"/>` +
				`</message>`,
			want: []string{"alice@example.com"},
		},
		{
			name: "non-mention type ignored",
			raw: `<message from="alice@example.com" type="chat">` +
				`<reference xmlns="urn:xmpp:reference:0" type="data" uri="xmpp:alice@example.com"/>` +
				`</message>`,
			want: nil,
		},
		{
			name: "non-xmpp uri ignored",
			raw: `<message from="alice@example.com" type="chat">` +
				`<reference xmlns="urn:xmpp:reference:0" type="mention" uri="https://example.com/alice"/>` +
				`</message>`,
			want: nil,
		},
		{
			name: "comma pseudo-jid ignored",
			raw: `<message from="alice@example.com" type="groupchat">` +
				`<reference xmlns="urn:xmpp:reference:0" type="mention" uri="xmpp:+15550101100,+15550101200@cheogram.com"/>` +
				`</message>`,
			want: nil,
		},
		{
			name: "duplicates deduped",
			raw: `<message from="alice@example.com" type="chat">` +
				`<reference xmlns="urn:xmpp:reference:0" type="mention" uri="xmpp:alice@example.com"/>` +
				`<reference xmlns="urn:xmpp:reference:0" type="mention" uri="xmpp:alice@example.com"/>` +
				`</message>`,
			want: []string{"alice@example.com"},
		},
		{
			name: "no references",
			raw:  `<message from="alice@example.com" type="chat"><body>hi</body></message>`,
			want: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			msg := decodeTestMessage(t, tc.raw)
			got := xmppMentionJIDs(msg)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("xmppMentionJIDs() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestXMPPMentionJIDsValueExtension(t *testing.T) {
	t.Parallel()
	msg := stanza.NewMessage(stanza.Attrs{})
	msg.Extensions = append(msg.Extensions, XMPPReference{Type: "mention", URI: "xmpp:+15550101100@cheogram.com"})
	got := xmppMentionJIDs(msg)
	want := []string{"+15550101100@cheogram.com"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("xmppMentionJIDs() = %q, want %q", got, want)
	}
}

func TestXMPPReferenceForJID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		jid     string
		wantReq XMPPReference
		wantOK  bool
	}{
		{
			name:    "plain jid",
			jid:     "alice@example.com",
			wantReq: XMPPReference{Type: "mention", URI: "xmpp:alice@example.com"},
			wantOK:  true,
		},
		{
			name:    "phone number",
			jid:     "+15550101100",
			wantReq: XMPPReference{Type: "mention", URI: "xmpp:+15550101100@cheogram.com"},
			wantOK:  true,
		},
		{
			name:   "comma pseudo-jid rejected",
			jid:    "+15550101100,+15550101200@cheogram.com",
			wantOK: false,
		},
		{
			name:   "empty rejected",
			jid:    "",
			wantOK: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := xmppReferenceForJID(tc.jid)
			if ok != tc.wantOK {
				t.Fatalf("xmppReferenceForJID(%q) ok = %v, want %v", tc.jid, ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if got != tc.wantReq {
				t.Errorf("xmppReferenceForJID(%q) = %+v, want %+v", tc.jid, got, tc.wantReq)
			}
		})
	}
}

func TestXMPPMentionReferencesNilBridge(t *testing.T) {
	t.Parallel()
	nc := &MyNetworkClient{}
	mentions := &event.Mentions{UserIDs: []id.UserID{"@alice:example.com"}}
	if got := nc.xmppMentionReferences(mentions); got != nil {
		t.Errorf("xmppMentionReferences() with nil bridge = %v, want nil", got)
	}
	if got := nc.xmppMentionReferences(nil); got != nil {
		t.Errorf("xmppMentionReferences(nil) = %v, want nil", got)
	}
	if got := nc.xmppMentionReferences(&event.Mentions{}); got != nil {
		t.Errorf("xmppMentionReferences(empty) = %v, want nil", got)
	}
}

func TestXMPPMentionMXIDsNilBridge(t *testing.T) {
	t.Parallel()
	nc := &MyNetworkClient{}
	msg := decodeTestMessage(t, `<message from="alice@example.com" type="chat">`+
		`<reference xmlns="urn:xmpp:reference:0" type="mention" uri="xmpp:alice@example.com"/>`+
		`</message>`)
	if got := nc.xmppMentionMXIDs(context.Background(), msg); got != nil {
		t.Errorf("xmppMentionMXIDs() with nil bridge = %v, want nil", got)
	}
}
