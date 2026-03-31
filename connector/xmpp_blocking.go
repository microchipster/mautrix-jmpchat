package connector

import (
	"context"
	"encoding/xml"
	"fmt"
	"sort"
	"strings"
	"time"

	"gosrc.io/xmpp/stanza"
)

const (
	NSBlocking            = "urn:xmpp:blocking"
	NSReportingReasonSpam = "urn:xmpp:reporting:spam"
)

type XMPPBlock struct {
	XMLName xml.Name        `xml:"urn:xmpp:blocking block"`
	Items   []XMPPBlockItem `xml:"item"`
}

func (b *XMPPBlock) Namespace() string {
	return b.XMLName.Space
}

func (b *XMPPBlock) GetSet() *stanza.ResultSet {
	return nil
}

type XMPPBlockItem struct {
	XMLName xml.Name    `xml:"urn:xmpp:blocking item"`
	JID     string      `xml:"jid,attr,omitempty"`
	Report  *XMPPReport `xml:"report,omitempty"`
}

type XMPPReport struct {
	XMLName  xml.Name      `xml:"urn:xmpp:reporting:1 report"`
	Reason   string        `xml:"reason,attr,omitempty"`
	StanzaID *XMPPStanzaID `xml:"stanza-id,omitempty"`
}

func init() {
	stanza.TypeRegistry.MapExtension(stanza.PKTIQ, xml.Name{Space: NSBlocking, Local: "block"}, XMPPBlock{})
}

func (nc *MyNetworkClient) rosterContactList() []string {
	nc.rosterMu.RLock()
	defer nc.rosterMu.RUnlock()

	contacts := make([]string, 0, len(nc.rosterItems))
	for jid := range nc.rosterItems {
		jid = bareJID(strings.TrimSpace(jid))
		if jid == "" {
			continue
		}
		contacts = append(contacts, jid)
	}
	sort.Strings(contacts)
	return contacts
}

func (nc *MyNetworkClient) blockContact(ctx context.Context, contactJID string, reportSpam bool, serverMsgID string) error {
	if !nc.isXMPPConnected() {
		return fmt.Errorf("XMPP client not connected")
	}
	contactJID = bareJID(strings.TrimSpace(contactJID))
	if contactJID == "" {
		return fmt.Errorf("contact JID is empty")
	}

	block := &XMPPBlock{Items: []XMPPBlockItem{{JID: contactJID}}}
	if reportSpam {
		report := &XMPPReport{Reason: NSReportingReasonSpam}
		if serverMsgID != "" {
			report.StanzaID = &XMPPStanzaID{ID: serverMsgID}
		}
		block.Items[0].Report = report
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	iq, _ := stanza.NewIQ(stanza.Attrs{
		Type: stanza.IQTypeSet,
		Id:   fmt.Sprintf("block_%d", time.Now().UnixNano()),
	})
	iq.Payload = block

	ch, err := nc.xmppClient.SendIQ(ctx, iq)
	if err != nil {
		return err
	}
	select {
	case resp := <-ch:
		if resp.Type != stanza.IQTypeResult {
			if resp.Error.Text != "" {
				return fmt.Errorf("block failed: %s", resp.Error.Text)
			}
			return fmt.Errorf("block failed: %s", resp.Type)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
