package connector

import (
	"context"
	"encoding/xml"
	"fmt"
	"time"

	"github.com/rs/zerolog"
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
	if toJID == "" && withJID == "" {
		return nil, fmt.Errorf("MAM target JID is empty")
	}
	if max <= 0 {
		max = 100
	}

	queryID := fmt.Sprintf("mam_%d", time.Now().UnixNano())
	if nc.log.GetLevel() <= zerolog.DebugLevel {
		beforeValue := ""
		if before != nil {
			beforeValue = *before
		}
		nc.log.Debug().
			Str("query_id", queryID).
			Str("to_jid", toJID).
			Str("with_jid", withJID).
			Int("max", max).
			Str("before", beforeValue).
			Time("start", start).
			Bool("start_zero", start.IsZero()).
			Msg("Sending MAM query")
	}
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

	attrs := stanza.Attrs{
		Type: stanza.IQTypeSet,
		Id:   queryID,
	}
	if toJID != "" {
		attrs.To = toJID
	}
	iq, _ := stanza.NewIQ(attrs)
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
		if nc.log.GetLevel() <= zerolog.DebugLevel {
			set := fin.Set
			first := ""
			last := ""
			count := ""
			if set != nil {
				if set.First != nil {
					first = set.First.Content
				}
				if set.Last != nil {
					last = *set.Last
				}
				if set.Count != nil {
					count = fmt.Sprintf("%d", *set.Count)
				}
			}
			nc.log.Debug().
				Str("query_id", queryID).
				Bool("complete", fin.Complete).
				Str("first", first).
				Str("last", last).
				Str("count", count).
				Msg("Received MAM fin")
		}
		return fin, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (nc *MyNetworkClient) backfillMUC(ctx context.Context, roomJID string, start time.Time) error {
	return nc.backfillMAM(ctx, roomJID, "", start)
}

func (nc *MyNetworkClient) backfillDM(ctx context.Context, withJID string, start time.Time) error {
	meta, ok := nc.login.Metadata.(*LoginMetadata)
	if !ok || meta == nil || meta.RemoteUserID == "" {
		return fmt.Errorf("login metadata missing remote user id")
	}
	if withJID == "" {
		return fmt.Errorf("DM MAM target is empty")
	}
	return nc.backfillMAM(ctx, "", withJID, start)
}

func (nc *MyNetworkClient) backfillMAM(ctx context.Context, toJID string, withJID string, start time.Time) error {
	const pageSize = 100
	const maxPages = 10000

	before := ""
	if nc.log.GetLevel() <= zerolog.DebugLevel {
		nc.log.Debug().
			Str("to_jid", toJID).
			Str("with_jid", withJID).
			Int("page_size", pageSize).
			Time("start", start).
			Bool("start_zero", start.IsZero()).
			Msg("Starting MAM backfill")
	}
	for page := 0; page < maxPages; page++ {
		fin, err := nc.requestMAMPage(ctx, toJID, withJID, pageSize, start, &before)
		if err != nil {
			if nc.log.GetLevel() <= zerolog.DebugLevel {
				nc.log.Debug().
					Str("to_jid", toJID).
					Str("with_jid", withJID).
					Int("page", page).
					Err(err).
					Msg("MAM backfill request failed")
			}
			return err
		}
		if fin == nil || fin.Set == nil || fin.Set.First == nil || fin.Set.First.Content == "" {
			if nc.log.GetLevel() <= zerolog.DebugLevel {
				nc.log.Debug().
					Str("to_jid", toJID).
					Str("with_jid", withJID).
					Int("page", page).
					Msg("MAM backfill ended: missing result set or first")
			}
			break
		}
		if fin.Complete {
			if nc.log.GetLevel() <= zerolog.DebugLevel {
				nc.log.Debug().
					Str("to_jid", toJID).
					Str("with_jid", withJID).
					Int("page", page).
					Msg("MAM backfill complete")
			}
			break
		}
		before = fin.Set.First.Content
		if nc.log.GetLevel() <= zerolog.DebugLevel {
			nc.log.Debug().
				Str("to_jid", toJID).
				Str("with_jid", withJID).
				Int("page", page).
				Str("next_before", before).
				Msg("MAM backfill page scheduled")
		}
	}
	if nc.log.GetLevel() <= zerolog.DebugLevel {
		nc.log.Debug().
			Str("to_jid", toJID).
			Str("with_jid", withJID).
			Msg("Finished MAM backfill")
	}
	return nil
}
