package connector

import (
	"encoding/xml"

	"gosrc.io/xmpp/stanza"
)

// MUCUserInvite implements XEP-0045 mediated invitation payload.
type MUCUserInvite struct {
	XMLName  xml.Name          `xml:"http://jabber.org/protocol/muc#user x"`
	Invite   *MUCInvite        `xml:"invite,omitempty"`
	Decline  *MUCInviteDecline `xml:"decline,omitempty"`
	Password string            `xml:"password,omitempty"`
}

type MUCInvite struct {
	From   string `xml:"from,attr,omitempty"`
	To     string `xml:"to,attr,omitempty"`
	Reason string `xml:"reason,omitempty"`
}

type MUCInviteDecline struct {
	From   string `xml:"from,attr,omitempty"`
	To     string `xml:"to,attr,omitempty"`
	Reason string `xml:"reason,omitempty"`
}

// MUCDirectInvite implements XEP-0249 direct invitation payload.
type MUCDirectInvite struct {
	XMLName  xml.Name `xml:"jabber:x:conference x"`
	JID      string   `xml:"jid,attr"`
	Reason   string   `xml:"reason,attr,omitempty"`
	Password string   `xml:"password,attr,omitempty"`
	Continue string   `xml:"continue,attr,omitempty"`
	Thread   string   `xml:"thread,attr,omitempty"`
	From     string   `xml:"from,attr,omitempty"`
}

func init() {
	stanza.TypeRegistry.MapExtension(
		stanza.PKTMessage,
		xml.Name{Space: "http://jabber.org/protocol/muc#user", Local: "x"},
		MUCUserInvite{},
	)
	stanza.TypeRegistry.MapExtension(
		stanza.PKTMessage,
		xml.Name{Space: "jabber:x:conference", Local: "x"},
		MUCDirectInvite{},
	)
}
