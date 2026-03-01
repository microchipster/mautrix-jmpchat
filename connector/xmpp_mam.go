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
	_, err := nc.requestMAMPage(ctx, roomJID, "", max, start, nil)
	return err
}

func (nc *MyNetworkClient) requestMAMPage(ctx context.Context, toJID string, withJID string, max int, start time.Time, before *string) (*MAMFin, error) {
	if nc.xmppClient == nil {
		return nil, fmt.Errorf("XMPP client not connected")
	}
	if toJID == "" {
		return nil, fmt.Errorf("MAM target JID is empty")
	}
	if max <= 0 {
		max = 100
	}

	queryID := fmt.Sprintf("mam_%d", time.Now().UnixNano())
	fields := []XDataField{
		{Var: "FORM_TYPE", Type: "hidden", Values: []string{NSMAM}},
	}
	if withJID != "" {
		fields = append(fields, XDataField{Var: "with", Values: []string{withJID}})
	}
	if !start.IsZero() {
		fields = append(fields, XDataField{Var: "start", Values: []string{start.UTC().Format(time.RFC3339)}})
	}

	set := &stanza.ResultSet{Max: &max}
	if before != nil {
		set.Before = before
	}

	q := &MAMQuery{
		QueryID: queryID,
		Form: XData{
			Type:   "submit",
			Fields: fields,
		},
		Set: set,
	}

	iq, _ := stanza.NewIQ(stanza.Attrs{
		Type: stanza.IQTypeSet,
		To:   toJID,
		Id:   queryID,
	})
	iq.Payload = q

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ch, err := nc.xmppClient.SendIQ(ctx, iq)
	if err != nil {
		return nil, err
	}
	select {
	case resp := <-ch:
		if resp.Type != stanza.IQTypeResult {
			return nil, fmt.Errorf("MAM request failed: %s", resp.Type)
		}
		fin, ok := resp.Payload.(*MAMFin)
		if !ok || fin == nil {
			return nil, fmt.Errorf("unexpected MAM fin payload")
		}
		return fin, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (nc *MyNetworkClient) backfillMUC(ctx context.Context, roomJID string, start time.Time) error {
	return nc.backfillMAM(ctx, roomJID, "", start)
}

func (nc *MyNetworkClient) backfillDM(ctx context.Context, withJID string) error {
	meta, ok := nc.login.Metadata.(*LoginMetadata)
	if !ok || meta == nil || meta.RemoteUserID == "" {
		return fmt.Errorf("login metadata missing remote user id")
	}
	if withJID == "" {
		return fmt.Errorf("DM MAM target is empty")
	}
	return nc.backfillMAM(ctx, meta.RemoteUserID, withJID, time.Time{})
}

func (nc *MyNetworkClient) backfillMAM(ctx context.Context, toJID string, withJID string, start time.Time) error {
	const pageSize = 100
	const maxPages = 10000

	before := ""
	for page := 0; page < maxPages; page++ {
		fin, err := nc.requestMAMPage(ctx, toJID, withJID, pageSize, start, &before)
		if err != nil {
			return err
		}
		if fin == nil || fin.Set == nil || fin.Set.First == nil || fin.Set.First.Content == "" {
			break
		}
		if fin.Complete {
			break
		}
		before = fin.Set.First.Content
	}
	return nil
}
