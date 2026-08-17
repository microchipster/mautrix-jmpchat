package connector

import (
	"context"
	"encoding/xml"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"gosrc.io/xmpp/stanza"
)

const (
	NSMAM0  = "urn:xmpp:mam:0"
	NSMAM1  = "urn:xmpp:mam:1"
	NSMAM2  = "urn:xmpp:mam:2"
	NSMAM   = NSMAM2
	NSXData = "jabber:x:data"
	NSDelay = "urn:xmpp:delay"
)

var supportedMAMNamespaces = []string{NSMAM2, NSMAM1, NSMAM0}

type MAMQuery struct {
	XMLName xml.Name          `xml:"query"`
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
	XMLName      xml.Name     `xml:"jabber:x:data x"`
	Type         string       `xml:"type,attr,omitempty"`
	Title        string       `xml:"title,omitempty"`
	Instructions []string     `xml:"instructions,omitempty"`
	Fields       []XDataField `xml:"field"`
}

type XDataField struct {
	Var         string        `xml:"var,attr,omitempty"`
	Type        string        `xml:"type,attr,omitempty"`
	Label       string        `xml:"label,attr,omitempty"`
	Description string        `xml:"desc,omitempty"`
	Required    *XDataBoolean `xml:"required,omitempty"`
	Values      []string      `xml:"value"`
	Options     []XDataOption `xml:"option,omitempty"`
}

type XDataOption struct {
	Label string `xml:"label,attr,omitempty"`
	Value string `xml:"value,omitempty"`
}

type XDataBoolean struct{}

type MAMFin struct {
	XMLName  xml.Name          `xml:"fin"`
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
	XMLName   xml.Name      `xml:"result"`
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

type collectedMAMMessage struct {
	Message    stanza.Message
	Timestamp  time.Time
	IDOverride string
	IsFromMe   bool
}

func formatMAMTimestamp(ts time.Time) string {
	return ts.UTC().Format("2006-01-02T15:04:05.000Z")
}

func formatIQError(err *stanza.Err) string {
	if err == nil {
		return ""
	}
	parts := make([]string, 0, 3)
	if err.Type != "" {
		parts = append(parts, string(err.Type))
	}
	if err.Reason != "" {
		parts = append(parts, err.Reason)
	}
	if err.Text != "" {
		parts = append(parts, err.Text)
	}
	return strings.Join(parts, "/")
}

func isServiceUnavailableMAMError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "service-unavailable")
}

func includeMAMEndBound(toJID, withJID string) bool {
	return !(toJID == "" && withJID == "")
}

func init() {
	for _, ns := range supportedMAMNamespaces {
		stanza.TypeRegistry.MapExtension(stanza.PKTIQ, xml.Name{Space: ns, Local: "query"}, MAMQuery{})
		stanza.TypeRegistry.MapExtension(stanza.PKTIQ, xml.Name{Space: ns, Local: "fin"}, MAMFin{})
		stanza.TypeRegistry.MapExtension(stanza.PKTMessage, xml.Name{Space: ns, Local: "result"}, MAMResult{})
	}
}

func bestMAMNamespace(featureSet map[string]bool) string {
	if len(featureSet) == 0 {
		return ""
	}
	for _, ns := range supportedMAMNamespaces {
		if featureSet[ns] {
			return ns
		}
	}
	return ""
}

func (nc *MyNetworkClient) requestMAM(ctx context.Context, roomJID string, max int, start time.Time) error {
	_, _, err := nc.requestMAMPage(ctx, roomJID, "", max, start, nil, nil)
	return err
}

func (nc *MyNetworkClient) backfillAccountDMs(ctx context.Context, start time.Time) error {
	err := nc.backfillMAM(ctx, "", "", start)
	if !isServiceUnavailableMAMError(err) {
		return err
	}
	ownJID := nc.ownBareJID()
	if ownJID == "" {
		return err
	}
	nc.log.Info().Str("to_jid", ownJID).Msg("Retrying account DM MAM query against own bare JID after service-unavailable")
	return nc.backfillMAM(ctx, ownJID, "", start)
}

func (nc *MyNetworkClient) requestMAMPage(ctx context.Context, toJID string, withJID string, max int, start time.Time, before, after *string) (*MAMFin, []collectedMAMMessage, error) {
	if nc.xmppClient == nil {
		return nil, nil, fmt.Errorf("XMPP client not connected")
	}
	if max <= 0 {
		max = 100
	}
	mamNamespace := nc.mamNamespaceForTarget(toJID, withJID)

	queryID := fmt.Sprintf("mam_%d", time.Now().UnixNano())
	if nc.log.GetLevel() <= zerolog.DebugLevel {
		beforeValue := ""
		if before != nil {
			beforeValue = *before
		}
		afterValue := ""
		if after != nil {
			afterValue = *after
		}
		nc.log.Debug().
			Str("query_id", queryID).
			Str("to_jid", toJID).
			Str("with_jid", withJID).
			Str("namespace", mamNamespace).
			Int("max", max).
			Str("before", beforeValue).
			Str("after", afterValue).
			Time("start", start).
			Bool("start_zero", start.IsZero()).
			Msg("Sending MAM query")
	}
	fields := []XDataField{
		{Var: "FORM_TYPE", Type: "hidden", Values: []string{mamNamespace}},
	}
	if withJID != "" {
		fields = append(fields, XDataField{Var: "with", Values: []string{withJID}})
	}
	if !start.IsZero() {
		fields = append(fields, XDataField{Var: "start", Values: []string{formatMAMTimestamp(start)}})
	}
	nc.postConnectMu.Lock()
	sessionEstablishedAt := nc.sessionEstablishedAt
	nc.postConnectMu.Unlock()
	if includeMAMEndBound(toJID, withJID) && !sessionEstablishedAt.IsZero() {
		fields = append(fields, XDataField{Var: "end", Values: []string{formatMAMTimestamp(sessionEstablishedAt)}})
	}

	set := &stanza.ResultSet{Max: &max}
	if before != nil {
		set.Before = before
	}
	if after != nil {
		set.After = after
	}

	q := &MAMQuery{
		XMLName: xml.Name{Space: mamNamespace, Local: "query"},
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
	nc.mamQueryMu.Lock()
	if nc.mamQueryResults == nil {
		nc.mamQueryResults = make(map[string][]collectedMAMMessage)
	}
	nc.mamQueryResults[queryID] = nil
	nc.mamQueryMu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ch, err := nc.xmppClient.SendIQ(ctx, iq)
	if err != nil {
		nc.mamQueryMu.Lock()
		delete(nc.mamQueryResults, queryID)
		nc.mamQueryMu.Unlock()
		return nil, nil, err
	}
	select {
	case resp := <-ch:
		results := nc.takeMAMQueryResults(queryID)
		sort.SliceStable(results, func(i, j int) bool {
			if results[i].Timestamp.Equal(results[j].Timestamp) {
				return results[i].IDOverride < results[j].IDOverride
			}
			return results[i].Timestamp.Before(results[j].Timestamp)
		})
		if resp.Type != stanza.IQTypeResult {
			if nc.log.GetLevel() <= zerolog.DebugLevel {
				nc.log.Debug().
					Str("query_id", queryID).
					Str("type", string(resp.Type)).
					Interface("xmpp_error", resp.Error).
					Interface("payload", resp.Payload).
					Msg("MAM request returned non-result IQ")
			}
			detail := formatIQError(resp.Error)
			if detail != "" {
				return nil, nil, fmt.Errorf("MAM request failed: %s (%s)", resp.Type, detail)
			}
			return nil, nil, fmt.Errorf("MAM request failed: %s", resp.Type)
		}
		fin, ok := resp.Payload.(*MAMFin)
		if !ok || fin == nil {
			return nil, nil, fmt.Errorf("unexpected MAM fin payload")
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
		return fin, results, nil
	case <-ctx.Done():
		nc.takeMAMQueryResults(queryID)
		return nil, nil, ctx.Err()
	}
}

func (nc *MyNetworkClient) takeMAMQueryResults(queryID string) []collectedMAMMessage {
	nc.mamQueryMu.Lock()
	defer nc.mamQueryMu.Unlock()
	results := nc.mamQueryResults[queryID]
	delete(nc.mamQueryResults, queryID)
	return results
}

func (nc *MyNetworkClient) storeMAMQueryResult(queryID string, result collectedMAMMessage) bool {
	nc.mamQueryMu.Lock()
	defer nc.mamQueryMu.Unlock()
	if nc.mamQueryResults == nil {
		return false
	}
	if _, ok := nc.mamQueryResults[queryID]; !ok {
		return false
	}
	nc.mamQueryResults[queryID] = append(nc.mamQueryResults[queryID], result)
	return true
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
	after := ""
	forward := !start.IsZero()
	mucArchive := withJID == "" && toJID != "" && bareJID(toJID) != nc.ownBareJID()
	if nc.log.GetLevel() <= zerolog.DebugLevel {
		nc.log.Debug().
			Str("to_jid", toJID).
			Str("with_jid", withJID).
			Int("page_size", pageSize).
			Time("start", start).
			Bool("forward", forward).
			Bool("start_zero", start.IsZero()).
			Msg("Starting MAM backfill")
	}
	for page := 0; page < maxPages; page++ {
		var beforePtr *string
		var afterPtr *string
		if forward {
			if after != "" {
				afterPtr = &after
			}
		} else {
			beforePtr = &before
		}
		fin, results, err := nc.requestMAMPage(ctx, toJID, withJID, pageSize, start, beforePtr, afterPtr)
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
		for _, result := range results {
			nc.handleIncomingXMPPMessage(result.Message, result.Timestamp, result.IDOverride, result.IsFromMe, true)
		}
		if fin == nil || fin.Set == nil {
			if nc.log.GetLevel() <= zerolog.DebugLevel {
				nc.log.Debug().
					Str("to_jid", toJID).
					Str("with_jid", withJID).
					Int("page", page).
					Msg("MAM backfill ended: missing result set")
			}
			if mucArchive {
				nc.maybeFallbackToMUCHistory(toJID, start, fin)
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
			if mucArchive {
				nc.maybeFallbackToMUCHistory(toJID, start, fin)
			}
			break
		}
		if forward {
			if fin.Set.Last == nil || *fin.Set.Last == "" {
				if nc.log.GetLevel() <= zerolog.DebugLevel {
					nc.log.Debug().
						Str("to_jid", toJID).
						Str("with_jid", withJID).
						Int("page", page).
						Msg("MAM catchup ended: missing result set last")
				}
				break
			}
			after = *fin.Set.Last
		} else {
			if fin.Set.First == nil || fin.Set.First.Content == "" {
				if nc.log.GetLevel() <= zerolog.DebugLevel {
					nc.log.Debug().
						Str("to_jid", toJID).
						Str("with_jid", withJID).
						Int("page", page).
						Msg("MAM backfill ended: missing result set first")
				}
				if mucArchive {
					nc.maybeFallbackToMUCHistory(toJID, start, fin)
				}
				break
			}
			before = fin.Set.First.Content
		}
		if nc.log.GetLevel() <= zerolog.DebugLevel {
			nextCursor := before
			cursorField := "next_before"
			if forward {
				nextCursor = after
				cursorField = "next_after"
			}
			nc.log.Debug().
				Str("to_jid", toJID).
				Str("with_jid", withJID).
				Int("page", page).
				Str(cursorField, nextCursor).
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
