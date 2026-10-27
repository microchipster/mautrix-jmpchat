// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"encoding/xml"
	"strings"

	"gosrc.io/xmpp/stanza"
)

// NSPubSubEvent is the namespace of pubsub event notifications.
const NSPubSubEvent = "http://jabber.org/protocol/pubsub#event"

// XMPPPubSubEvent carries a pubsub event payload (e.g. XEP-0402 bookmark
// updates published to our PEP node). The inner XML is decoded separately.
type XMPPPubSubEvent struct {
	stanza.MsgExtension
	XMLName  xml.Name `xml:"http://jabber.org/protocol/pubsub#event event"`
	InnerXML string   `xml:",innerxml"`
}

func init() {
	stanza.TypeRegistry.MapExtension(stanza.PKTMessage, xml.Name{Space: NSPubSubEvent, Local: "event"}, XMPPPubSubEvent{})
}

type bookmarkPEPItem struct {
	ID         string               `xml:"id,attr"`
	Retract    *struct{}            `xml:"retract"`
	Conference *BookmarkConference2 `xml:"urn:xmpp:bookmarks:1 conference"`
}

type bookmarkPEPItems struct {
	Node  string            `xml:"node,attr"`
	Items []bookmarkPEPItem `xml:"item"`
}

// parseBookmarkPEPEvent decodes a pubsub event payload into changed XEP-0402
// bookmarks and retracted bookmark IDs. The boolean reports whether the event
// contained any bookmark items at all.
func parseBookmarkPEPEvent(innerXML string) ([]BookmarkConference, []string, bool) {
	innerXML = strings.TrimSpace(innerXML)
	if innerXML == "" {
		return nil, nil, false
	}
	var items bookmarkPEPItems
	if err := xml.Unmarshal([]byte(innerXML), &items); err != nil {
		return nil, nil, false
	}
	if items.Node != NSBookmarks2 {
		return nil, nil, false
	}
	var conferences []BookmarkConference
	var removed []string
	found := false
	for _, item := range items.Items {
		found = true
		if item.Retract != nil {
			if id := strings.TrimSpace(item.ID); id != "" {
				removed = append(removed, id)
			}
			continue
		}
		if item.Conference == nil || strings.TrimSpace(item.Conference.JID) == "" {
			continue
		}
		conferences = append(conferences, BookmarkConference{
			JID:      strings.TrimSpace(item.Conference.JID),
			Name:     item.Conference.Name,
			Autojoin: item.Conference.Autojoin,
			Nick:     item.Conference.Nick,
			Password: item.Conference.Password,
		})
	}
	return conferences, removed, found
}

// handleBookmarkPEP processes live XEP-0402 bookmark updates published to our
// PEP node and joined/left rooms accordingly. It reports whether the stanza
// was a bookmark event (and therefore should not be bridged as a message).
func (nc *MyNetworkClient) handleBookmarkPEP(msg stanza.Message) bool {
	var event XMPPPubSubEvent
	if !msg.Get(&event) {
		return false
	}
	if strings.TrimSpace(stanza.StringValue(msg.Body)) != "" {
		return false
	}
	conferences, removed, found := parseBookmarkPEPEvent(event.InnerXML)
	if !found {
		return false
	}
	nc.log.Debug().
		Int("conferences", len(conferences)).
		Int("removed", len(removed)).
		Msg("Received XEP-0402 bookmark update")

	for _, roomJID := range removed {
		// The bookmark is already gone on the server: drop the local record
		// before leaving, so leaveMUC does not republish an autojoin-off item.
		if nc.bookmarkedRooms != nil {
			delete(nc.bookmarkedRooms, roomJID)
		}
		if nc.bookmarkedRoomNames != nil {
			delete(nc.bookmarkedRoomNames, roomJID)
		}
		nc.leaveMUC(roomJID)
	}
	var toJoin []BookmarkConference
	for _, conf := range conferences {
		if nc.isNonMUCRoomJID(conf.JID) {
			// Cheogram gateway JIDs and the login's own JID are not MUC
			// rooms: a bookmark event for them must not trigger a join or a
			// leave presence.
			nc.log.Debug().
				Str("room_jid", conf.JID).
				Msg("Skipping non-MUC JID from bookmark event")
			continue
		}
		if !conf.AutojoinEnabled() {
			nc.leaveMUC(conf.JID)
			if nc.bookmarkedRooms != nil {
				delete(nc.bookmarkedRooms, conf.JID)
			}
			continue
		}
		if nc.bookmarkedRooms == nil {
			nc.bookmarkedRooms = map[string]bool{}
		}
		nc.bookmarkedRooms[conf.JID] = true
		nc.rejoinMUC(conf.JID)
		toJoin = append(toJoin, conf)
	}
	if len(toJoin) > 0 {
		nc.handleBookmarkConferences(toJoin, "bookmarks2_event")
	}
	return true
}
