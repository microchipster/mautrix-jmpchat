package connector

import (
	"encoding/xml"
	"strings"

	"gosrc.io/xmpp/stanza"
)

const (
	NSReference = "urn:xmpp:reference:0"
	NSSIMS      = "urn:xmpp:sims:1"
	NSFallback  = "urn:xmpp:fallback:0"
	NSMucUser   = "http://jabber.org/protocol/muc#user"
	NSReactions = "urn:xmpp:reactions:0"
	NSEME       = "urn:xmpp:eme:0"
	NSCarbons   = "urn:xmpp:carbons:2"
	NSReceipts  = "urn:xmpp:receipts"
	NSMarkers   = "urn:xmpp:chat-markers:0"
	NSOriginID  = "urn:xmpp:origin-id:0"
	NSStanzaID  = "urn:xmpp:stanza-id:0"
	NSOccID     = "urn:xmpp:occupant-id:0"
	NSDelayOld  = "jabber:x:delay"
	NSEvent     = "jabber:x:event"
	NSConf      = "jabber:x:conference"
	NSReply     = "urn:xmpp:reply:0"
)

type XMPPReference struct {
	stanza.MsgExtension
	XMLName      xml.Name          `xml:"urn:xmpp:reference:0 reference"`
	Type         string            `xml:"type,attr,omitempty"`
	URI          string            `xml:"uri,attr,omitempty"`
	MediaSharing *XMPPMediaSharing `xml:"urn:xmpp:sims:1 media-sharing"`
}

type XMPPMediaSharing struct {
	stanza.MsgExtension
	XMLName xml.Name         `xml:"urn:xmpp:sims:1 media-sharing"`
	FileV5  *XMPPSIMSFile    `xml:"urn:xmpp:jingle:apps:file-transfer:5 file"`
	FileV4  *XMPPSIMSFile    `xml:"urn:xmpp:jingle:apps:file-transfer:4 file"`
	FileV3  *XMPPSIMSFile    `xml:"urn:xmpp:jingle:apps:file-transfer:3 file"`
	Sources *XMPPSIMSSources `xml:"urn:xmpp:sims:1 sources"`
}

func (ms *XMPPMediaSharing) File() *XMPPSIMSFile {
	if ms == nil {
		return nil
	}
	if ms.FileV5 != nil {
		return ms.FileV5
	}
	if ms.FileV4 != nil {
		return ms.FileV4
	}
	return ms.FileV3
}

type XMPPSIMSSources struct {
	Reference *XMPPSIMSReference `xml:"urn:xmpp:reference:0 reference"`
}

type XMPPSIMSReference struct {
	URI string `xml:"uri,attr,omitempty"`
}

type XMPPSIMSFile struct {
	XMLName   xml.Name `xml:"file"`
	Name      string   `xml:"name,omitempty"`
	MediaType string   `xml:"media-type,omitempty"`
	Size      int64    `xml:"size,omitempty"`
	Width     int      `xml:"https://schema.org/ width,omitempty"`
	Height    int      `xml:"https://schema.org/ height,omitempty"`
	Duration  string   `xml:"https://schema.org/ duration,omitempty"`
}

type XMPPFallback struct {
	stanza.MsgExtension
	XMLName xml.Name           `xml:"urn:xmpp:fallback:0 fallback"`
	For     string             `xml:"for,attr,omitempty"`
	Body    []XMPPFallbackBody `xml:"body"`
}

type XMPPFallbackBody struct {
	Start int `xml:"start,attr,omitempty"`
	End   int `xml:"end,attr,omitempty"`
}

type XMPPReply struct {
	stanza.MsgExtension
	XMLName xml.Name `xml:"urn:xmpp:reply:0 reply"`
	To      string   `xml:"to,attr,omitempty"`
	ID      string   `xml:"id,attr,omitempty"`
}

type XMPPMucUser struct {
	stanza.MsgExtension
	XMLName  xml.Name `xml:"http://jabber.org/protocol/muc#user x"`
	InnerXML string   `xml:",innerxml"`
}

type XMPPReactions struct {
	stanza.MsgExtension
	XMLName xml.Name `xml:"urn:xmpp:reactions:0 reactions"`
	ID      string   `xml:"id,attr,omitempty"`
	Reacts  []string `xml:"reaction"`
}

func (r XMPPReactions) First() string {
	for _, reaction := range r.Reacts {
		reaction = strings.TrimSpace(reaction)
		if reaction != "" {
			return reaction
		}
	}
	return ""
}

func (r XMPPReactions) All() []string {
	seen := make(map[string]struct{}, len(r.Reacts))
	cleaned := make([]string, 0, len(r.Reacts))
	for _, reaction := range r.Reacts {
		reaction = strings.TrimSpace(reaction)
		if reaction == "" {
			continue
		}
		if _, ok := seen[reaction]; ok {
			continue
		}
		seen[reaction] = struct{}{}
		cleaned = append(cleaned, reaction)
	}
	return cleaned
}

type XMPPEME struct {
	stanza.MsgExtension
	XMLName  xml.Name `xml:"urn:xmpp:eme:0 encryption"`
	InnerXML string   `xml:",innerxml"`
}

type XMPCarbonSent struct {
	stanza.MsgExtension
	XMLName   xml.Name          `xml:"urn:xmpp:carbons:2 sent"`
	Forwarded *stanza.Forwarded `xml:"forwarded"`
}

type XMPCarbonReceived struct {
	stanza.MsgExtension
	XMLName   xml.Name          `xml:"urn:xmpp:carbons:2 received"`
	Forwarded *stanza.Forwarded `xml:"forwarded"`
}

type XMPPReceipt struct {
	stanza.MsgExtension
	XMLName xml.Name `xml:"urn:xmpp:receipts request"`
}

type XMPPReceiptReceived struct {
	stanza.MsgExtension
	XMLName xml.Name `xml:"urn:xmpp:receipts received"`
	ID      string   `xml:"id,attr,omitempty"`
}

type XMPPMarker struct {
	stanza.MsgExtension
	XMLName xml.Name `xml:"urn:xmpp:chat-markers:0 markable"`
}

type XMPPMarkerReceived struct {
	stanza.MsgExtension
	XMLName xml.Name `xml:"urn:xmpp:chat-markers:0 received"`
	ID      string   `xml:"id,attr,omitempty"`
}

type XMPPMarkerDisplayed struct {
	stanza.MsgExtension
	XMLName xml.Name `xml:"urn:xmpp:chat-markers:0 displayed"`
	ID      string   `xml:"id,attr,omitempty"`
}

type XMPPMarkerAcknowledged struct {
	stanza.MsgExtension
	XMLName xml.Name `xml:"urn:xmpp:chat-markers:0 acknowledged"`
	ID      string   `xml:"id,attr,omitempty"`
}

type XMPPOriginID struct {
	stanza.MsgExtension
	XMLName xml.Name `xml:"urn:xmpp:origin-id:0 origin-id"`
	ID      string   `xml:"id,attr,omitempty"`
}

type XMPPStanzaID struct {
	stanza.MsgExtension
	XMLName xml.Name `xml:"urn:xmpp:stanza-id:0 stanza-id"`
	ID      string   `xml:"id,attr,omitempty"`
	By      string   `xml:"by,attr,omitempty"`
}

type XMPOccupantID struct {
	stanza.MsgExtension
	XMLName xml.Name `xml:"urn:xmpp:occupant-id:0 occupant-id"`
	ID      string   `xml:"id,attr,omitempty"`
}

type XMPPDelay struct {
	stanza.MsgExtension
	XMLName xml.Name `xml:"urn:xmpp:delay delay"`
	Stamp   string   `xml:"stamp,attr,omitempty"`
}

type XMPPDelayOld struct {
	stanza.MsgExtension
	XMLName  xml.Name `xml:"jabber:x:delay x"`
	InnerXML string   `xml:",innerxml"`
}

type XMPPEvent struct {
	stanza.MsgExtension
	XMLName  xml.Name `xml:"jabber:x:event x"`
	InnerXML string   `xml:",innerxml"`
}

type XMPPConference struct {
	stanza.MsgExtension
	XMLName  xml.Name `xml:"jabber:x:conference x"`
	InnerXML string   `xml:",innerxml"`
}

func init() {
	stanza.TypeRegistry.MapExtension(stanza.PKTMessage, xml.Name{Space: NSReference, Local: "reference"}, XMPPReference{})
	stanza.TypeRegistry.MapExtension(stanza.PKTMessage, xml.Name{Space: NSSIMS, Local: "media-sharing"}, XMPPMediaSharing{})
	stanza.TypeRegistry.MapExtension(stanza.PKTMessage, xml.Name{Space: NSFallback, Local: "fallback"}, XMPPFallback{})
	stanza.TypeRegistry.MapExtension(stanza.PKTMessage, xml.Name{Space: NSReply, Local: "reply"}, XMPPReply{})
	stanza.TypeRegistry.MapExtension(stanza.PKTMessage, xml.Name{Space: NSMucUser, Local: "x"}, XMPPMucUser{})
	stanza.TypeRegistry.MapExtension(stanza.PKTMessage, xml.Name{Space: NSReactions, Local: "reactions"}, XMPPReactions{})
	stanza.TypeRegistry.MapExtension(stanza.PKTMessage, xml.Name{Space: NSEME, Local: "encryption"}, XMPPEME{})
	stanza.TypeRegistry.MapExtension(stanza.PKTMessage, xml.Name{Space: NSCarbons, Local: "sent"}, XMPCarbonSent{})
	stanza.TypeRegistry.MapExtension(stanza.PKTMessage, xml.Name{Space: NSCarbons, Local: "received"}, XMPCarbonReceived{})
	stanza.TypeRegistry.MapExtension(stanza.PKTMessage, xml.Name{Space: NSReceipts, Local: "request"}, XMPPReceipt{})
	stanza.TypeRegistry.MapExtension(stanza.PKTMessage, xml.Name{Space: NSReceipts, Local: "received"}, XMPPReceiptReceived{})
	stanza.TypeRegistry.MapExtension(stanza.PKTMessage, xml.Name{Space: NSMarkers, Local: "markable"}, XMPPMarker{})
	stanza.TypeRegistry.MapExtension(stanza.PKTMessage, xml.Name{Space: NSMarkers, Local: "received"}, XMPPMarkerReceived{})
	stanza.TypeRegistry.MapExtension(stanza.PKTMessage, xml.Name{Space: NSMarkers, Local: "displayed"}, XMPPMarkerDisplayed{})
	stanza.TypeRegistry.MapExtension(stanza.PKTMessage, xml.Name{Space: NSMarkers, Local: "acknowledged"}, XMPPMarkerAcknowledged{})
	stanza.TypeRegistry.MapExtension(stanza.PKTMessage, xml.Name{Space: NSOriginID, Local: "origin-id"}, XMPPOriginID{})
	stanza.TypeRegistry.MapExtension(stanza.PKTMessage, xml.Name{Space: NSStanzaID, Local: "stanza-id"}, XMPPStanzaID{})
	stanza.TypeRegistry.MapExtension(stanza.PKTMessage, xml.Name{Space: NSOccID, Local: "occupant-id"}, XMPOccupantID{})
	stanza.TypeRegistry.MapExtension(stanza.PKTMessage, xml.Name{Space: NSDelay, Local: "delay"}, XMPPDelay{})
	stanza.TypeRegistry.MapExtension(stanza.PKTMessage, xml.Name{Space: NSDelayOld, Local: "x"}, XMPPDelayOld{})
	stanza.TypeRegistry.MapExtension(stanza.PKTMessage, xml.Name{Space: NSEvent, Local: "x"}, XMPPEvent{})
	stanza.TypeRegistry.MapExtension(stanza.PKTMessage, xml.Name{Space: NSConf, Local: "x"}, XMPPConference{})
}
