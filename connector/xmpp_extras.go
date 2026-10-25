// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"fmt"
	"strconv"
	"strings"

	"gosrc.io/xmpp/stanza"
)

// normalizeGeoURI returns the trimmed input when it looks like a valid
// "geo:" URI with numeric latitude/longitude, and an empty string otherwise.
func normalizeGeoURI(body string) string {
	trimmed := strings.TrimSpace(body)
	if len(trimmed) < len("geo:1,2") || !strings.EqualFold(trimmed[:4], "geo:") {
		return ""
	}
	coords := trimmed[4:]
	// Strip URI parameters such as ";u=35".
	if idx := strings.IndexByte(coords, ';'); idx >= 0 {
		coords = coords[:idx]
	}
	parts := strings.SplitN(coords, ",", 3)
	if len(parts) < 2 {
		return ""
	}
	lat := strings.TrimSpace(parts[0])
	lon := strings.TrimSpace(parts[1])
	if lat == "" || lon == "" {
		return ""
	}
	if _, err := strconv.ParseFloat(lat, 64); err != nil {
		return ""
	}
	if _, err := strconv.ParseFloat(lon, 64); err != nil {
		return ""
	}
	return trimmed
}

// handleIncomingHeadlineMessage consumes XMPP headline messages that carry
// service status text (for example outage notices from the SMS gateway) and
// forwards them to the management room instead of bridging them as chats.
// Jingle call messages are left for the call handler. The return value
// reports whether the message was consumed.
func (nc *MyNetworkClient) handleIncomingHeadlineMessage(msg stanza.Message, bareFromJid string) bool {
	if msg.Type != stanza.MessageTypeHeadline {
		return false
	}
	if extractXMPPCallEvent(msg) != nil {
		return false
	}
	body := strings.TrimSpace(stanza.StringValue(msg.Body))
	if body == "" {
		return true
	}
	from := strings.TrimSpace(bareFromJid)
	if from == "" {
		from = "server"
	}
	nc.log.Info().
		Str("from", from).
		Str("body", body).
		Msg("Received XMPP headline/service status message")
	nc.notifyManagement(fmt.Sprintf("Service status from %s: %s", from, body))
	return true
}
