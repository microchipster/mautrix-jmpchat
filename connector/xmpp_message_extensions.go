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

func init() {
	stanza.TypeRegistry.MapExtension(stanza.PKTMessage, xml.Name{Space: NSReference, Local: "reference"}, XMPPReference{})
	stanza.TypeRegistry.MapExtension(stanza.PKTMessage, xml.Name{Space: NSSIMS, Local: "media-sharing"}, XMPPSIMS{})
	stanza.TypeRegistry.MapExtension(stanza.PKTMessage, xml.Name{Space: NSFallback, Local: "fallback"}, XMPPFallback{})
	stanza.TypeRegistry.MapExtension(stanza.PKTMessage, xml.Name{Space: NSMucUser, Local: "x"}, XMPPMucUser{})
	stanza.TypeRegistry.MapExtension(stanza.PKTMessage, xml.Name{Space: NSReactions, Local: "reactions"}, XMPPReactions{})
	stanza.TypeRegistry.MapExtension(stanza.PKTMessage, xml.Name{Space: NSEME, Local: "encryption"}, XMPPEME{})
}
