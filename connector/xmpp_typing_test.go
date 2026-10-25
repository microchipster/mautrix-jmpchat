// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"encoding/xml"
	"testing"

	"gosrc.io/xmpp/stanza"
)

func TestShouldSendXMPPTyping(t *testing.T) {
	t.Parallel()
	tests := map[string]bool{
		"+15550101100@cheogram.com":              false,
		"+15550101100,+15550101200@cheogram.com": false,
		"+15550101100@CHEOGRAM.COM":              false,
		"alice@example.com":                      true,
		"room@conference.example.com":            true,
		"room@cheogram.com.example.com":          true,
		"":                                       false,
	}

	for jid, want := range tests {
		if got := shouldSendXMPPTyping(jid); got != want {
			t.Errorf("shouldSendXMPPTyping(%q) = %v, want %v", jid, got, want)
		}
	}
}

func TestExtractXMPPChatState(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		extension  stanza.MsgExtension
		wantTyping bool
	}{
		{"composing", &stanza.StateComposing{}, true},
		{"paused", &stanza.StatePaused{}, false},
		{"active", &stanza.StateActive{}, false},
		{"inactive", &stanza.StateInactive{}, false},
		{"gone", &stanza.StateGone{}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			msg := stanza.NewMessage(stanza.Attrs{})
			msg.Extensions = append(msg.Extensions, tc.extension)
			gotTyping, gotOK := extractXMPPChatState(msg)
			if !gotOK {
				t.Fatalf("extractXMPPChatState() did not detect %s", tc.name)
			}
			if gotTyping != tc.wantTyping {
				t.Errorf("extractXMPPChatState() typing = %v, want %v", gotTyping, tc.wantTyping)
			}
		})
	}

	msg := stanza.NewMessage(stanza.Attrs{})
	if _, ok := extractXMPPChatState(msg); ok {
		t.Errorf("extractXMPPChatState() detected a chat state on a message without extensions")
	}
}

func TestExtractXMPPChatStateFromXML(t *testing.T) {
	t.Parallel()
	raw := `<message from="alice@example.com" to="me@example.com" type="chat">` +
		`<composing xmlns="http://jabber.org/protocol/chatstates"/>` +
		`</message>`

	var msg stanza.Message
	if err := xml.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatalf("failed to unmarshal message: %v", err)
	}
	gotTyping, gotOK := extractXMPPChatState(msg)
	if !gotOK {
		t.Fatalf("extractXMPPChatState() did not detect the composing extension")
	}
	if !gotTyping {
		t.Errorf("extractXMPPChatState() typing = false, want true")
	}
}

func TestExtractXMPPChatStateWithBody(t *testing.T) {
	t.Parallel()
	msg := stanza.NewMessage(stanza.Attrs{})
	msg.Body = stanza.StringPtr("hello")
	msg.Extensions = append(msg.Extensions, &stanza.StateComposing{})

	if _, ok := extractXMPPChatState(msg); !ok {
		t.Fatalf("extractXMPPChatState() did not detect the composing extension")
	}
}
