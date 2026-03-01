package connector

import (
	"context"
	"encoding/xml"
	"fmt"
	"time"

	"gosrc.io/xmpp/stanza"
)

const (
	NSMAM   = "urn:xmpp:mam:2"
	NSXData = "jabber:x:data"
	NSDelay = "urn:xmpp:delay"
)

type MAMQuery struct {
	XMLName xml.Name          `xml:"urn:xmpp:mam:2 query"`
	QueryID string            `xml:"queryid,attr,omitempty"`
	Form    XData             `xml:"x"`
	Set     *stanza.ResultSet `xml:"set,omitempty"`
}

func (q *MAMQuery) Namespace() string {
	return q.XMLName.Space
}

func (q *MAMQuery) GetSet() *stanza.ResultSet {
	return q.Set
}

type XData struct {
	XMLName xml.Name     `xml:"jabber:x:data x"`
	Type    string       `xml:"type,attr,omitempty"`
	Fields  []XDataField `xml:"field"`
}

type XDataField struct {
	Var    string   `xml:"var,attr,omitempty"`
	Type   string   `xml:"type,attr,omitempty"`
	Values []string `xml:"value"`
}

type MAMFin struct {
	XMLName  xml.Name          `xml:"urn:xmpp:mam:2 fin"`
	Complete bool              `xml:"complete,attr,omitempty"`
	Set      *stanza.ResultSet `xml:"set,omitempty"`
}

func (f *MAMFin) Namespace() string {
	return f.XMLName.Space
}

func (f *MAMFin) GetSet() *stanza.ResultSet {
	return f.Set
}

type MAMResult struct {
	stanza.MsgExtension
	XMLName   xml.Name      `xml:"urn:xmpp:mam:2 result"`
	QueryID   string        `xml:"queryid,attr,omitempty"`
	ID        string        `xml:"id,attr,omitempty"`
	Forwarded *MAMForwarded `xml:"forwarded"`
}

type MAMForwarded struct {
	XMLName xml.Name        `xml:"urn:xmpp:forward:0 forwarded"`
	Delay   *MAMDelay       `xml:"delay"`
	Message *stanza.Message `xml:"message"`
}

type MAMDelay struct {
	XMLName xml.Name `xml:"urn:xmpp:delay delay"`
	Stamp   string   `xml:"stamp,attr,omitempty"`
}

func init() {
	stanza.TypeRegistry.MapExtension(stanza.PKTIQ, xml.Name{Space: NSMAM, Local: "query"}, MAMQuery{})
	stanza.TypeRegistry.MapExtension(stanza.PKTIQ, xml.Name{Space: NSMAM, Local: "fin"}, MAMFin{})
	stanza.TypeRegistry.MapExtension(stanza.PKTMessage, xml.Name{Space: NSMAM, Local: "result"}, MAMResult{})
}

func (nc *MyNetworkClient) requestMAM(ctx context.Context, roomJID string, max int, start time.Time) error {
	if nc.xmppClient == nil {
		return fmt.Errorf("XMPP client not connected")
	}
	if roomJID == "" {
		return fmt.Errorf("MAM room JID is empty")
	}
	if max <= 0 {
		max = 100
	}

	queryID := fmt.Sprintf("mam_%d", time.Now().UnixNano())
	fields := []XDataField{
		{Var: "FORM_TYPE", Type: "hidden", Values: []string{NSMAM}},
	}
	if !start.IsZero() {
		fields = append(fields, XDataField{Var: "start", Values: []string{start.UTC().Format(time.RFC3339)}})
	}

	q := &MAMQuery{
		QueryID: queryID,
		Form: XData{
			Type:   "submit",
			Fields: fields,
		},
		Set: &stanza.ResultSet{Max: &max},
	}

	iq, _ := stanza.NewIQ(stanza.Attrs{
		Type: stanza.IQTypeSet,
		To:   roomJID,
		Id:   queryID,
	})
	iq.Payload = q

	if err := nc.xmppClient.Send(iq); err != nil {
		return err
	}
	return nil
}
