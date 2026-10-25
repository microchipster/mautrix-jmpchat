// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"encoding/xml"
	"testing"

	"gosrc.io/xmpp/stanza"
)

func TestEncryptedXMPPKind(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		raw      string
		wantKind string
		wantOK   bool
	}{
		{
			name:     "omemo",
			raw:      `<message type="chat"><encrypted xmlns="eu.siacs.conversations.axolotl"><header sid="1"/></encrypted></message>`,
			wantKind: "OMEMO",
			wantOK:   true,
		},
		{
			name:     "openpgp",
			raw:      `<message type="chat"><x xmlns="jabber:x:encrypted">PGP DATA</x></message>`,
			wantKind: "OpenPGP",
			wantOK:   true,
		},
		{
			name:     "eme omemo namespace",
			raw:      `<message type="chat"><encryption xmlns="urn:xmpp:eme:0" namespace="eu.siacs.conversations.axolotl"/></message>`,
			wantKind: "OMEMO",
			wantOK:   true,
		},
		{
			name:     "eme unknown namespace",
			raw:      `<message type="chat"><encryption xmlns="urn:xmpp:eme:0" namespace="urn:example:custom"/></message>`,
			wantKind: "end-to-end encryption",
			wantOK:   true,
		},
		{
			name:     "aesgcm body",
			raw:      `<message type="chat"><body>aesgcm://example.com/abc#key</body></message>`,
			wantKind: "AES-GCM media",
			wantOK:   true,
		},
		{
			name:   "plain message",
			raw:    `<message type="chat"><body>hello</body></message>`,
			wantOK: false,
		},
		{
			name:   "empty body",
			raw:    `<message type="chat"><body></body></message>`,
			wantOK: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var msg stanza.Message
			if err := xml.Unmarshal([]byte(tc.raw), &msg); err != nil {
				t.Fatalf("failed to unmarshal message: %v", err)
			}
			kind, ok := encryptedXMPPKind(msg)
			if ok != tc.wantOK {
				t.Fatalf("encryptedXMPPKind() ok = %v, want %v", ok, tc.wantOK)
			}
			if kind != tc.wantKind {
				t.Errorf("encryptedXMPPKind() kind = %q, want %q", kind, tc.wantKind)
			}
		})
	}
}

func TestEncryptedXMPPNotice(t *testing.T) {
	t.Parallel()
	notice := encryptedXMPPNotice("OMEMO")
	if notice == "" {
		t.Fatalf("encryptedXMPPNotice() returned an empty string")
	}
	if got := encryptedXMPPNotice(""); got == "" {
		t.Errorf("encryptedXMPPNotice(\"\") returned an empty string")
	}
}
