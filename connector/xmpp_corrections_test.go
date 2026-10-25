// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"encoding/xml"
	"testing"

	"gosrc.io/xmpp/stanza"
)

func decodeTestMessage(t *testing.T, raw string) stanza.Message {
	t.Helper()
	var msg stanza.Message
	if err := xml.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatalf("failed to unmarshal message: %v", err)
	}
	return msg
}

func TestXMPPCorrectionDecode(t *testing.T) {
	t.Parallel()
	raw := `<message from="alice@example.com" to="me@example.com" type="chat" id="correction-1">` +
		`<body>fixed text</body>` +
		`<replace xmlns="urn:xmpp:message-correct:0" id="original-1"/>` +
		`</message>`
	msg := decodeTestMessage(t, raw)

	var replace XMPPReplace
	if !msg.Get(&replace) {
		t.Fatalf("XMPPReplace extension was not decoded")
	}
	if replace.ID != "original-1" {
		t.Errorf("replace.ID = %q, want %q", replace.ID, "original-1")
	}
	if stanza.StringValue(msg.Body) != "fixed text" {
		t.Errorf("body = %q, want %q", stanza.StringValue(msg.Body), "fixed text")
	}
}

func TestXMPPRetractionDecode(t *testing.T) {
	t.Parallel()
	raw := `<message from="alice@example.com" to="me@example.com" type="chat" id="retract-1">` +
		`<retract xmlns="urn:xmpp:retraction:0" id="original-1"/>` +
		`<retracted xmlns="urn:xmpp:retraction:0" id="original-1"/>` +
		`</message>`
	msg := decodeTestMessage(t, raw)

	var retract XMPPRetract
	if !msg.Get(&retract) {
		t.Fatalf("XMPPRetract extension was not decoded")
	}
	if retract.ID != "original-1" {
		t.Errorf("retract.ID = %q, want %q", retract.ID, "original-1")
	}
	if !hasXMPPRetraction(msg) {
		t.Errorf("hasXMPPRetraction() = false, want true")
	}
}

func TestXMPPModeratedDecode(t *testing.T) {
	t.Parallel()
	raw := `<message from="room@conference.example.com" to="me@example.com" type="groupchat">` +
		`<moderated xmlns="urn:xmpp:message-moderate:0" id="moderated-1">` +
		`<retract xmlns="urn:xmpp:message-moderate:0" id="stanza-42"/>` +
		`</moderated>` +
		`</message>`
	msg := decodeTestMessage(t, raw)

	var moderated XMPPModerated
	if !msg.Get(&moderated) {
		t.Fatalf("XMPPModerated extension was not decoded")
	}
	if moderated.ID != "moderated-1" {
		t.Errorf("moderated.ID = %q, want %q", moderated.ID, "moderated-1")
	}
	if !hasXMPPRetraction(msg) {
		t.Errorf("hasXMPPRetraction() = false, want true")
	}
}

func TestHasXMPPRetractionFalse(t *testing.T) {
	t.Parallel()
	msg := stanza.NewMessage(stanza.Attrs{})
	if hasXMPPRetraction(msg) {
		t.Errorf("hasXMPPRetraction() = true for a plain message")
	}
	var emptyRetract stanza.Message
	emptyRetract.Extensions = append(emptyRetract.Extensions, &XMPPRetract{})
	if hasXMPPRetraction(emptyRetract) {
		t.Errorf("hasXMPPRetraction() = true for a retraction without an ID")
	}
}

func TestXMPPMessageAliases(t *testing.T) {
	t.Parallel()
	raw := `<message from="alice@example.com" to="me@example.com" type="chat" id="plain-1">` +
		`<body>hello</body>` +
		`<origin-id xmlns="urn:xmpp:origin-id:0" id="origin-1"/>` +
		`<stanza-id xmlns="urn:xmpp:stanza-id:0" id="stanza-1" by="alice@example.com"/>` +
		`</message>`
	msg := decodeTestMessage(t, raw)

	originID, stanzaID, plainID := xmppMessageAliases(msg)
	if originID != "origin-1" {
		t.Errorf("originID = %q, want %q", originID, "origin-1")
	}
	if stanzaID != "stanza-1" {
		t.Errorf("stanzaID = %q, want %q", stanzaID, "stanza-1")
	}
	if plainID != "plain-1" {
		t.Errorf("plainID = %q, want %q", plainID, "plain-1")
	}

	empty := stanza.NewMessage(stanza.Attrs{})
	originID, stanzaID, plainID = xmppMessageAliases(empty)
	if originID != "" || stanzaID != "" || plainID != "" {
		t.Errorf("xmppMessageAliases() on an empty message = (%q, %q, %q), want empty strings", originID, stanzaID, plainID)
	}
}
