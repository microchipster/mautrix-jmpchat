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
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/id"
)

// XMPPBlocklist is the XEP-0191 blocklist result: the JIDs the account has
// blocked or reported as spam on the XMPP server.
type XMPPBlocklist struct {
	XMLName xml.Name        `xml:"urn:xmpp:blocking blocklist"`
	Items   []XMPPBlockItem `xml:"item"`
}

// Namespace implements stanza.IQPayload.
func (b *XMPPBlocklist) Namespace() string {
	return b.XMLName.Space
}

// GetSet implements stanza.IQPayload.
func (b *XMPPBlocklist) GetSet() *stanza.ResultSet {
	return nil
}

func init() {
	stanza.TypeRegistry.MapExtension(stanza.PKTIQ, xml.Name{Space: NSBlocking, Local: "blocklist"}, XMPPBlocklist{})
}

// fetchBlockedJIDs returns the bare JIDs on the server-side XEP-0191 blocklist.
func (nc *MyNetworkClient) fetchBlockedJIDs(ctx context.Context) ([]string, error) {
	if !nc.isXMPPConnected() {
		return nil, fmt.Errorf("XMPP client not connected")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	iq, _ := stanza.NewIQ(stanza.Attrs{
		Type: stanza.IQTypeGet,
		Id:   fmt.Sprintf("blocklist_%d", time.Now().UnixNano()),
	})
	iq.Payload = &XMPPBlocklist{}

	ch, err := nc.xmppClient.SendIQ(ctx, iq)
	if err != nil {
		return nil, err
	}
	select {
	case resp := <-ch:
		if resp.Type != stanza.IQTypeResult {
			if resp.Error.Text != "" {
				return nil, fmt.Errorf("blocklist request failed: %s", resp.Error.Text)
			}
			return nil, fmt.Errorf("blocklist request failed: %s", resp.Type)
		}
		blocklist, _ := resp.Payload.(*XMPPBlocklist)
		if blocklist == nil {
			return nil, fmt.Errorf("blocklist response was missing")
		}
		seen := map[string]struct{}{}
		var jids []string
		for _, item := range blocklist.Items {
			jid := bareJID(strings.TrimSpace(item.JID))
			if jid == "" || !strings.Contains(jid, "@") {
				continue
			}
			if _, ok := seen[jid]; ok {
				continue
			}
			seen[jid] = struct{}{}
			jids = append(jids, jid)
		}
		sort.Strings(jids)
		return jids, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// spamRoomMatch is one bridged room whose contact is on the blocklist.
type spamRoomMatch struct {
	jid      string
	portalID string
	portal   *bridgev2.Portal
}

// blockedContactRooms finds this login's portals for contacts on the blocklist.
func blockedContactRooms(ce *commands.Event, login *bridgev2.UserLogin, blocked []string) ([]spamRoomMatch, error) {
	keys := make(map[string]string, len(blocked))
	for _, jid := range blocked {
		keys[EscapeJID(CanonicalBareJID(jid))] = jid
	}
	rows, err := ce.Bridge.DB.UserPortal.GetAllForLogin(ce.Ctx, login.UserLogin)
	if err != nil {
		return nil, fmt.Errorf("failed to list portals: %w", err)
	}
	var matches []spamRoomMatch
	for _, up := range rows {
		portal, err := ce.Bridge.GetExistingPortalByKey(ce.Ctx, up.Portal)
		if err != nil || portal == nil || portal.MXID == "" {
			continue
		}
		if ce.User.ManagementRoom != "" && portal.MXID == ce.User.ManagementRoom {
			continue
		}
		if spaceRoom, err := login.GetSpaceRoom(ce.Ctx); err == nil && spaceRoom != "" && portal.MXID == spaceRoom {
			continue
		}
		jid, ok := keys[string(up.Portal.ID)]
		if !ok {
			jid, ok = keys[string(portal.OtherUserID)]
		}
		if !ok {
			continue
		}
		matches = append(matches, spamRoomMatch{jid: jid, portalID: string(up.Portal.ID), portal: portal})
	}
	return matches, nil
}

// CommandDeleteSpamRooms deletes the bridged rooms of contacts the user has
// blocked or reported as spam (the XMPP server-side blocklist).
var CommandDeleteSpamRooms = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		login := ce.User.GetDefaultLogin()
		if login == nil {
			ce.Reply("No login found")
			return
		}
		client, ok := login.Client.(*MyNetworkClient)
		if !ok || client == nil {
			ce.Reply("This command is only available on the JMP connection.")
			return
		}
		blocked, err := client.fetchBlockedJIDs(ce.Ctx)
		if err != nil {
			ce.Reply("Failed to fetch your XMPP blocklist: %v", err)
			return
		}
		if len(blocked) == 0 {
			ce.Reply("Your XMPP blocklist is empty. Use `report <jid>` in a room to block and report a contact as spam.")
			return
		}
		matches, err := blockedContactRooms(ce, login, blocked)
		if err != nil {
			ce.Reply("%v", err)
			return
		}
		if len(matches) == 0 {
			ce.Reply("No bridged rooms found for the %d contact(s) on your blocklist.", len(blocked))
			return
		}
		confirm := len(ce.Args) > 0 && (ce.Args[0] == "yes" || ce.Args[0] == "--yes")
		if !confirm {
			lines := []string{fmt.Sprintf("Found %d bridged room(s) for %d blocked/reported contact(s):", len(matches), len(blocked))}
			for _, m := range matches {
				lines = append(lines, fmt.Sprintf("- %s — %s", m.jid, m.portal.MXID))
			}
			lines = append(lines, "Run: delete-spam-rooms yes to delete them.")
			ce.Reply("%s", strings.Join(lines, "\n"))
			return
		}
		portals := make([]*bridgev2.Portal, 0, len(matches))
		matchedIDs := make(map[string]struct{}, len(matches))
		var callRooms []id.RoomID
		for _, m := range matches {
			portals = append(portals, m.portal)
			matchedIDs[m.portalID] = struct{}{}
			if meta, ok := m.portal.Metadata.(*PortalMetadata); ok && meta.CallRoomMXID != "" {
				callRooms = append(callRooms, meta.CallRoomMXID)
			}
		}
		bridgev2.DeleteManyPortals(ce.Ctx, portals, nil)
		rows, err := ce.Bridge.DB.UserPortal.GetAllForLogin(ce.Ctx, login.UserLogin)
		if err != nil {
			ce.Reply("Failed to list portals for cleanup: %v", err)
			return
		}
		for _, up := range rows {
			if _, ok := matchedIDs[string(up.Portal.ID)]; ok {
				_ = ce.Bridge.DB.UserPortal.Delete(ce.Ctx, up)
			}
		}
		for _, roomID := range callRooms {
			_ = ce.Bridge.Bot.DeleteRoom(ce.Ctx, roomID, false)
		}
		ce.Reply("Deleted %d bridged room(s) for contacts on your blocklist (and %d call room(s)). The management room and space are kept.", len(matches), len(callRooms))
	},
	Name: "delete-spam-rooms",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionAdmin,
		Description: "Remove bridged rooms for contacts you blocked or reported as spam (preview; use: delete-spam-rooms yes)",
	},
	RequiresLogin: true,
}
