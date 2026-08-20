// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"encoding/xml"
	"testing"
	"time"

	"gosrc.io/xmpp/stanza"
)

func TestXMPPMessageTimestampUsesInnerDelay(t *testing.T) {
	fallback := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	want := time.Date(2026, 8, 15, 10, 11, 12, 345000000, time.UTC)
	msg := stanza.Message{Extensions: []stanza.MsgExtension{
		&XMPPDelay{Stamp: "2026-08-15T10:11:12.345Z"},
	}}

	if got := xmppMessageTimestamp(msg, fallback); !got.Equal(want) {
		t.Fatalf("timestamp = %s, want %s", got, want)
	}
}

func TestBuildIncomingXMPPReceiptResponse(t *testing.T) {
	msg := stanza.Message{
		Attrs: stanza.Attrs{From: "alice@example.com/mobile", Type: stanza.MessageTypeChat, Id: "message-1"},
		Extensions: []stanza.MsgExtension{
			&XMPPReceipt{},
		},
	}
	response, ok := buildIncomingXMPPReceiptResponse(msg, false, false)
	if !ok {
		t.Fatal("expected receipt response")
	}
	if response.To != msg.From || response.Type != msg.Type {
		t.Fatalf("response target/type = %q/%q, want %q/%q", response.To, response.Type, msg.From, msg.Type)
	}
	var received XMPPReceiptReceived
	if !response.Get(&received) || received.ID != msg.Id {
		t.Fatalf("receipt response ID = %q, want %q", received.ID, msg.Id)
	}

	if _, ok := buildIncomingXMPPReceiptResponse(msg, false, true); ok {
		t.Fatal("did not expect response to MAM replay")
	}
	if _, ok := buildIncomingXMPPReceiptResponse(msg, true, false); ok {
		t.Fatal("did not expect response to own carbon")
	}
}

func TestCarbonForwardedUnmarshalingWithDelay(t *testing.T) {
	xmlData := `<sent xmlns="urn:xmpp:carbons:2">
		<forwarded xmlns="urn:xmpp:forward:0">
			<delay xmlns="urn:xmpp:delay" stamp="2026-08-15T10:11:12.345Z"/>
			<message xmlns="jabber:client" from="user@cheogram.com" to="+15551234567@cheogram.com" type="chat" id="carbon-1">
				<body>Hello via Carbon Sent</body>
			</message>
		</forwarded>
	</sent>`

	var carbonSent XMPCarbonSent
	if err := xml.Unmarshal([]byte(xmlData), &carbonSent); err != nil {
		t.Fatalf("failed to unmarshal XMPCarbonSent: %v", err)
	}

	if carbonSent.Forwarded == nil {
		t.Fatal("carbonSent.Forwarded is nil")
	}
	if carbonSent.Forwarded.Delay == nil || carbonSent.Forwarded.Delay.Stamp != "2026-08-15T10:11:12.345Z" {
		t.Fatalf("delay stamp = %v, want 2026-08-15T10:11:12.345Z", carbonSent.Forwarded.Delay)
	}
	if carbonSent.Forwarded.Message == nil {
		t.Fatal("carbonSent.Forwarded.Message is nil")
	}
	if carbonSent.Forwarded.Message.To != "+15551234567@cheogram.com" {
		t.Fatalf("message to = %q, want +15551234567@cheogram.com", carbonSent.Forwarded.Message.To)
	}
	if carbonSent.Forwarded.Message.Body == nil || *carbonSent.Forwarded.Message.Body != "Hello via Carbon Sent" {
		t.Fatalf("message body = %v, want 'Hello via Carbon Sent'", carbonSent.Forwarded.Message.Body)
	}
}

func TestCarbonReceivedUnmarshalingWithDelay(t *testing.T) {
	xmlData := `<received xmlns="urn:xmpp:carbons:2">
		<forwarded xmlns="urn:xmpp:forward:0">
			<delay xmlns="urn:xmpp:delay" stamp="2026-08-15T10:11:12.345Z"/>
			<message xmlns="jabber:client" from="+15551234567@cheogram.com" to="user@cheogram.com/res" type="chat" id="carbon-2">
				<body>Hello via Carbon Received</body>
			</message>
		</forwarded>
	</received>`

	var carbonReceived XMPCarbonReceived
	if err := xml.Unmarshal([]byte(xmlData), &carbonReceived); err != nil {
		t.Fatalf("failed to unmarshal XMPCarbonReceived: %v", err)
	}

	if carbonReceived.Forwarded == nil {
		t.Fatal("carbonReceived.Forwarded is nil")
	}
	if carbonReceived.Forwarded.Delay == nil || carbonReceived.Forwarded.Delay.Stamp != "2026-08-15T10:11:12.345Z" {
		t.Fatalf("delay stamp = %v, want 2026-08-15T10:11:12.345Z", carbonReceived.Forwarded.Delay)
	}
	if carbonReceived.Forwarded.Message == nil {
		t.Fatal("carbonReceived.Forwarded.Message is nil")
	}
	if carbonReceived.Forwarded.Message.From != "+15551234567@cheogram.com" {
		t.Fatalf("message from = %q, want +15551234567@cheogram.com", carbonReceived.Forwarded.Message.From)
	}
	if carbonReceived.Forwarded.Message.Body == nil || *carbonReceived.Forwarded.Message.Body != "Hello via Carbon Received" {
		t.Fatalf("message body = %v, want 'Hello via Carbon Received'", carbonReceived.Forwarded.Message.Body)
	}
}
