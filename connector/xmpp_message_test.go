// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
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
