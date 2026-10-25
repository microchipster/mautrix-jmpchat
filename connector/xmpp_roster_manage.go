// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

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

// rosterIQTimeout bounds roster management IQ round trips.
const rosterIQTimeout = 30 * time.Second

// rosterItemsQuery builds the jabber:iq:roster query payload for roster
// management. Go's encoding/xml needs the element name from XMLName, so it is
// set explicitly instead of relying on the struct tag.
func rosterItemsQuery() *stanza.RosterItems {
	return &stanza.RosterItems{
		XMLName: xml.Name{Space: "jabber:iq:roster", Local: "query"},
	}
}

// setRosterItem adds or updates a roster entry (including its display name)
// and refreshes the contact naming pipeline afterwards.
func (nc *MyNetworkClient) setRosterItem(ctx context.Context, jid, name string) error {
	if err := nc.setRosterItemCore(ctx, jid, name); err != nil {
		return err
	}
	nc.refreshRosterNamedPortals()
	return nil
}

// setRosterItemCore adds or updates a roster entry without refreshing room
// names, so bulk callers (e.g. CardDAV import) can refresh once at the end.
func (nc *MyNetworkClient) setRosterItemCore(ctx context.Context, jid, name string) error {
	jid = CanonicalBareJID(jid)
	if jid == "" {
		return fmt.Errorf("invalid JID")
	}
	if !nc.isXMPPConnected() {
		return fmt.Errorf("not connected to the XMPP server")
	}
	ctx, cancel := context.WithTimeout(ctx, rosterIQTimeout)
	defer cancel()

	iq, err := stanza.NewIQ(stanza.Attrs{
		Type: stanza.IQTypeSet,
		To:   nc.ownBareJID(),
		Id:   fmt.Sprintf("roster_set_%d", time.Now().UnixNano()),
	})
	if err != nil {
		return err
	}
	items := rosterItemsQuery()
	items.AddItem(jid, "", "", name, nil)
	iq.Payload = items

	ch, err := nc.xmppClient.SendIQ(ctx, iq)
	if err != nil {
		return err
	}
	select {
	case resp := <-ch:
		if resp.Type != stanza.IQTypeResult {
			return fmt.Errorf("server rejected the roster update (response type %s)", resp.Type)
		}
	case <-ctx.Done():
		return ctx.Err()
	}

	nc.markRosterJID(jid, true)
	if name != "" {
		nc.updateRosterContactName(jid, name)
	}
	return nil
}

// removeRosterItem removes a roster entry and forgets any locally known name.
func (nc *MyNetworkClient) removeRosterItem(ctx context.Context, jid string) error {
	jid = CanonicalBareJID(jid)
	if jid == "" {
		return fmt.Errorf("invalid JID")
	}
	if !nc.isXMPPConnected() {
		return fmt.Errorf("not connected to the XMPP server")
	}
	ctx, cancel := context.WithTimeout(ctx, rosterIQTimeout)
	defer cancel()

	iq, err := stanza.NewIQ(stanza.Attrs{
		Type: stanza.IQTypeSet,
		To:   nc.ownBareJID(),
		Id:   fmt.Sprintf("roster_remove_%d", time.Now().UnixNano()),
	})
	if err != nil {
		return err
	}
	items := rosterItemsQuery()
	items.AddItem(jid, "remove", "", "", nil)
	iq.Payload = items

	ch, err := nc.xmppClient.SendIQ(ctx, iq)
	if err != nil {
		return err
	}
	select {
	case resp := <-ch:
		if resp.Type != stanza.IQTypeResult {
			return fmt.Errorf("server rejected the roster removal (response type %s)", resp.Type)
		}
	case <-ctx.Done():
		return ctx.Err()
	}

	nc.markRosterJID(jid, false)
	nc.refreshRosterNamedPortals()
	return nil
}

// markRosterJID adds (or removes) a JID in the locally tracked roster set.
func (nc *MyNetworkClient) markRosterJID(jid string, present bool) {
	jid = CanonicalBareJID(jid)
	if jid == "" {
		return
	}
	nc.rosterMu.Lock()
	defer nc.rosterMu.Unlock()
	if present {
		if nc.rosterItems == nil {
			nc.rosterItems = map[string]bool{}
		}
		nc.rosterItems[jid] = true
		return
	}
	delete(nc.rosterItems, jid)
}

// rosterEntryMatches reports whether a query matches a roster entry by JID or
// display name. An empty query matches everything.
func rosterEntryMatches(query, jid, name string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return true
	}
	if strings.Contains(strings.ToLower(jid), query) {
		return true
	}
	return strings.Contains(strings.ToLower(name), query)
}

// searchRoster returns formatted matching contacts (display name plus JID),
// sorted for stable output.
func (nc *MyNetworkClient) searchRoster(ctx context.Context, query string) []string {
	entries := make([]string, 0)
	for _, jid := range nc.rosterContactList() {
		name := contactDisplayName(ctx, nc.bridge, jid)
		if !rosterEntryMatches(query, jid, name) {
			continue
		}
		if name == "" || name == jid {
			entries = append(entries, jid)
		} else {
			entries = append(entries, fmt.Sprintf("%s (%s)", name, jid))
		}
	}
	sort.Strings(entries)
	return entries
}
