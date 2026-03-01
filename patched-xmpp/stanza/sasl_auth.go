package stanza

import "encoding/xml"

type SASLAuth struct {
	XMLName   xml.Name `xml:"urn:ietf:params:xml:ns:xmpp-sasl auth"`
	Mechanism string   `xml:"mechanism,attr"`
	Value     string   `xml:",innerxml"`
}

type SASLSuccess struct {
	XMLName xml.Name `xml:"urn:ietf:params:xml:ns:xmpp-sasl success"`
	Value   string   `xml:",innerxml"`
}

func (SASLSuccess) Name() string {
	return "sasl:success"
}

type saslSuccessDecoder struct{}

var saslSuccess saslSuccessDecoder

func (saslSuccessDecoder) decode(p *xml.Decoder, se xml.StartElement) (SASLSuccess, error) {
	var packet SASLSuccess
	err := p.DecodeElement(&packet, &se)
	return packet, err
}

type SASLFailure struct {
	XMLName xml.Name `xml:"urn:ietf:params:xml:ns:xmpp-sasl failure"`
	Any     xml.Name // error reason is a subelement
}

func (SASLFailure) Name() string {
	return "sasl:failure"
}

type saslFailureDecoder struct{}

var saslFailure saslFailureDecoder

func (saslFailureDecoder) decode(p *xml.Decoder, se xml.StartElement) (SASLFailure, error) {
	var packet SASLFailure
	err := p.DecodeElement(&packet, &se)
	return packet, err
}

type Bind struct {
	XMLName  xml.Name `xml:"urn:ietf:params:xml:ns:xmpp-bind bind"`
	Resource string   `xml:"resource,omitempty"`
	Jid      string   `xml:"jid,omitempty"`
	ResultSet *ResultSet `xml:"set,omitempty"`
}

func (b *Bind) Namespace() string {
	return b.XMLName.Space
}

func (b *Bind) GetSet() *ResultSet {
	return b.ResultSet
}

type StreamSession struct {
	XMLName  xml.Name  `xml:"urn:ietf:params:xml:ns:xmpp-session session"`
	Optional *struct{} // If element does exist, it mean we are not required to open session
	ResultSet *ResultSet `xml:"set,omitempty"`
}

func (s *StreamSession) Namespace() string {
	return s.XMLName.Space
}

func (s *StreamSession) GetSet() *ResultSet {
	return s.ResultSet
}

func (s *StreamSession) IsOptional() bool {
	if s.XMLName.Local == "session" {
		return s.Optional != nil
	}
	return true
}

func init() {
	TypeRegistry.MapExtension(PKTIQ, xml.Name{Space: "urn:ietf:params:xml:ns:xmpp-bind", Local: "bind"}, Bind{})
	TypeRegistry.MapExtension(PKTIQ, xml.Name{Space: "urn:ietf:params:xml:ns:xmpp-session", Local: "session"}, StreamSession{})
}
