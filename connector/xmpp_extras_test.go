// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"encoding/xml"
	"testing"

	"gosrc.io/xmpp/stanza"
)

func TestNormalizeGeoURI(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "plain geo uri", body: "geo:48.2082,16.3738", want: "geo:48.2082,16.3738"},
		{name: "with parameters", body: "geo:48.2082,16.3738;u=35", want: "geo:48.2082,16.3738;u=35"},
		{name: "uppercase scheme", body: "GEO:1,2", want: "GEO:1,2"},
		{name: "surrounding whitespace", body: "  geo:48.2082,16.3738  ", want: "geo:48.2082,16.3738"},
		{name: "negative coords", body: "geo:-33.8688,151.2093", want: "geo:-33.8688,151.2093"},
		{name: "missing longitude", body: "geo:48.2082", want: ""},
		{name: "non-numeric latitude", body: "geo:abc,16.3738", want: ""},
		{name: "non-numeric longitude", body: "geo:48.2082,abc", want: ""},
		{name: "plain text", body: "hello world", want: ""},
		{name: "empty", body: "", want: ""},
		{name: "scheme only", body: "geo:", want: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := normalizeGeoURI(tc.body); got != tc.want {
				t.Errorf("normalizeGeoURI(%q) = %q, want %q", tc.body, got, tc.want)
			}
		})
	}
}

func TestHandleIncomingHeadlineMessage(t *testing.T) {
	t.Parallel()
	nc := &MyNetworkClient{}

	status := decodeTestMessage(t, `<message type="headline" from="cheogram.com"><body>Service outage notice</body></message>`)
	if !nc.handleIncomingHeadlineMessage(status, "cheogram.com") {
		t.Errorf("expected status headline to be consumed")
	}

	empty := decodeTestMessage(t, `<message type="headline" from="cheogram.com"></message>`)
	if !nc.handleIncomingHeadlineMessage(empty, "cheogram.com") {
		t.Errorf("expected body-less headline to be consumed")
	}

	chat := decodeTestMessage(t, `<message type="chat" from="alice@example.com"><body>hi</body></message>`)
	if nc.handleIncomingHeadlineMessage(chat, "alice@example.com") {
		t.Errorf("expected chat message not to be consumed")
	}

	jingle := stanza.Message{
		Type: stanza.MessageTypeHeadline,
		Extensions: []stanza.MsgExtension{&XMPPJinglePropose{
			ID:           "sess-1",
			Descriptions: []XMPPJingleDescription{{XMLName: xml.Name{Space: NSJingleRTP, Local: "description"}}},
		}},
	}
	if nc.handleIncomingHeadlineMessage(jingle, "alice@example.com") {
		t.Errorf("expected jingle headline not to be consumed")
	}
}
