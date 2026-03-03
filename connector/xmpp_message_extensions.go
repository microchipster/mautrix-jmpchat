package connector

import (
	"encoding/xml"

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
)

type XMPPReference struct {
	stanza.MsgExtension
	XMLName  xml.Name `xml:"urn:xmpp:reference:0 reference"`
	InnerXML string   `xml:",innerxml"`
}

type XMPPSIMS struct {
	stanza.MsgExtension
	XMLName  xml.Name `xml:"urn:xmpp:sims:1 media-sharing"`
	InnerXML string   `xml:",innerxml"`
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

type XMPPMucUser struct {
	stanza.MsgExtension
	XMLName  xml.Name `xml:"http://jabber.org/protocol/muc#user x"`
	InnerXML string   `xml:",innerxml"`
}

type XMPPReactions struct {
	stanza.MsgExtension
	XMLName  xml.Name `xml:"urn:xmpp:reactions:0 reactions"`
	InnerXML string   `xml:",innerxml"`
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
	XMLName  xml.Name `xml:"urn:xmpp:receipts request"`
	InnerXML string   `xml:",innerxml"`
}

type XMPPReceiptReceived struct {
	stanza.MsgExtension
	XMLName  xml.Name `xml:"urn:xmpp:receipts received"`
	InnerXML string   `xml:",innerxml"`
}

type XMPPMarker struct {
	stanza.MsgExtension
	XMLName  xml.Name `xml:"urn:xmpp:chat-markers:0 markable"`
	InnerXML string   `xml:",innerxml"`
}

type XMPPMarkerReceived struct {
	stanza.MsgExtension
	XMLName  xml.Name `xml:"urn:xmpp:chat-markers:0 received"`
	InnerXML string   `xml:",innerxml"`
}

type XMPPMarkerDisplayed struct {
	stanza.MsgExtension
	XMLName  xml.Name `xml:"urn:xmpp:chat-markers:0 displayed"`
	InnerXML string   `xml:",innerxml"`
}

type XMPPMarkerAcknowledged struct {
	stanza.MsgExtension
	XMLName  xml.Name `xml:"urn:xmpp:chat-markers:0 acknowledged"`
	InnerXML string   `xml:",innerxml"`
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
	stanza.TypeRegistry.MapExtension(stanza.PKTMessage, xml.Name{Space: NSSIMS, Local: "media-sharing"}, XMPPSIMS{})
	stanza.TypeRegistry.MapExtension(stanza.PKTMessage, xml.Name{Space: NSFallback, Local: "fallback"}, XMPPFallback{})
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
