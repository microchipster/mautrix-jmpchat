// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"fmt"
	"html"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.mau.fi/util/ptr"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/matrix"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/bridgev2/status"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

var CommandSync = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		login := ce.User.GetDefaultLogin()
		if login == nil {
			ce.Reply("No login found")
			return
		}
		client, ok := login.Client.(*MyNetworkClient)
		if !ok || client == nil {
			ce.Reply("Network client not available")
			return
		}
		full, lookback, err := parseSyncArgs(ce.Args)
		if err != nil {
			ce.Reply("%v", err)
			return
		}
		notify := func(msg string) {
			sendNotice(ce.Bot, ce.OrigRoomID, msg)
		}
		if err := client.StartFullSync(ce.Ctx, notify, full, lookback); err != nil {
			ce.Reply("%v", err)
			return
		}
		ce.Reply("Sync queued. I will post progress here.")
	},
	Name:    "sync",
	Aliases: []string{"sync-groups"},
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionChats,
		Description: "Sync roster + bookmarks and MAM backfill. Usage: sync [full|<duration>] (default: 30d; full also recreates empty rooms)",
	},
	RequiresLogin: true,
}

var CommandPing = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		login := ce.User.GetDefaultLogin()
		if login == nil {
			ce.Reply("No login found")
			return
		}
		client, ok := login.Client.(*MyNetworkClient)
		if !ok || client == nil {
			ce.Reply("Network client not available")
			return
		}

		state := loginBridgeStateSummary(login)
		conn := client.ConnectionStatus()
		ping := xmppPingSummary{Skipped: true, Detail: "not connected"}
		if conn.Connected {
			rtt, err := client.PingXMPP(ce.Ctx)
			if err != nil {
				ping = xmppPingSummary{Err: err}
			} else {
				ping = xmppPingSummary{RTT: rtt}
			}
		}

		ce.Reply(formatPingStatus(login, state, conn, ping))
	},
	Name:    "ping",
	Aliases: []string{"status", "login-status"},
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionChats,
		Description: "Show login connection state and ping the XMPP server",
	},
	RequiresLogin: true,
}

var CommandSyncStatus = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		login := ce.User.GetDefaultLogin()
		if login == nil {
			ce.Reply("No login found")
			return
		}
		client, ok := login.Client.(*MyNetworkClient)
		if !ok || client == nil {
			ce.Reply("Network client not available")
			return
		}
		running, status, lastErr, startedAt := client.SyncStatus()
		if status == "" {
			status = "idle"
		}
		if running {
			ce.Reply("Sync status: %s (running for %s)", status, time.Since(startedAt).Truncate(time.Second))
			return
		}
		if lastErr != "" {
			ce.Reply("Sync status: %s (last error: %s)", status, lastErr)
			return
		}
		ce.Reply("Sync status: %s", status)
	},
	Name: "sync-status",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionChats,
		Description: "Show full sync progress",
	},
	RequiresLogin: true,
}

type loginBridgeStateSummaryResult struct {
	State     string
	Timestamp string
	Message   string
	Error     string
}

type xmppPingSummary struct {
	RTT     time.Duration
	Err     error
	Skipped bool
	Detail  string
}

func loginBridgeStateSummary(login *bridgev2.UserLogin) loginBridgeStateSummaryResult {
	state := loginBridgeStateSummaryResult{State: "unknown", Timestamp: "unknown"}
	if login == nil || login.BridgeState == nil {
		return state
	}
	prev := login.BridgeState.GetPrev()
	if prev.StateEvent != "" {
		state.State = string(prev.StateEvent)
	}
	if !prev.Timestamp.Time.IsZero() {
		state.Timestamp = prev.Timestamp.Time.Local().Format(time.RFC3339)
	}
	state.Message = strings.TrimSpace(prev.Message)
	state.Error = string(prev.Error)
	return state
}

func formatPingStatus(login *bridgev2.UserLogin, state loginBridgeStateSummaryResult, conn loginConnectionStatus, ping xmppPingSummary) string {
	remote := "(unknown)"
	if login != nil {
		remote = strings.TrimSpace(login.RemoteName)
		if remote == "" {
			if meta, ok := login.Metadata.(*LoginMetadata); ok && meta != nil {
				remote = strings.TrimSpace(meta.RemoteUserID)
			}
		}
		if remote == "" {
			remote = "(unknown)"
		}
	}

	loginID := "(unknown)"
	if login != nil {
		loginID = string(login.ID)
	}

	lines := []string{
		fmt.Sprintf("Login: %s", loginID),
		fmt.Sprintf("Remote: %s", remote),
		fmt.Sprintf("Bridge state: %s at %s", state.State, state.Timestamp),
	}
	if state.Error != "" {
		lines = append(lines, fmt.Sprintf("Bridge error: %s", state.Error))
	}
	if state.Message != "" {
		lines = append(lines, fmt.Sprintf("Bridge message: %s", state.Message))
	}

	if conn.Connected {
		lines = append(lines, "XMPP session: connected")
	} else if conn.SessionEstablished {
		lines = append(lines, "XMPP session: reconnecting")
	} else {
		lines = append(lines, "XMPP session: disconnected")
	}
	if conn.BindJID != "" {
		lines = append(lines, fmt.Sprintf("Bound JID: %s", conn.BindJID))
	}
	if conn.Uptime > 0 {
		lines = append(lines, fmt.Sprintf("Session uptime: %s", conn.Uptime.Truncate(time.Second)))
	}
	if conn.ReconnectAttempt > 0 {
		lines = append(lines, fmt.Sprintf("Reconnect attempt: %d", conn.ReconnectAttempt))
	}

	switch {
	case ping.Err != nil:
		lines = append(lines, fmt.Sprintf("XMPP ping: failed (%v)", ping.Err))
	case ping.Skipped:
		if ping.Detail == "" {
			ping.Detail = "not available"
		}
		lines = append(lines, fmt.Sprintf("XMPP ping: skipped (%s)", ping.Detail))
	default:
		lines = append(lines, fmt.Sprintf("XMPP ping: ok (%s)", ping.RTT.Truncate(time.Millisecond)))
	}

	return strings.Join(lines, "\n")
}

var CommandStartChat = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		login := ce.User.GetDefaultLogin()
		if login == nil {
			ce.Reply("No login found")
			return
		}
		client, ok := login.Client.(*MyNetworkClient)
		if !ok || client == nil {
			ce.Reply("Network client not available")
			return
		}
		if len(ce.Args) == 0 {
			ce.Reply("Usage: create <phone or JID> [<phone or JID> ...]")
			return
		}

		name := ""
		args := ce.Args
		if len(args) >= 2 && args[0] == "--name" {
			name = args[1]
			args = args[2:]
		}
		if len(args) == 0 {
			ce.Reply("Usage: create <phone or JID> [<phone or JID> ...]")
			return
		}

		participants := make([]string, 0, len(args))
		for _, raw := range args {
			jid := normalizeJID(raw)
			if jid == "" {
				continue
			}
			participants = append(participants, jid)
		}
		if len(participants) == 0 {
			ce.Reply("No valid phone numbers or JIDs provided")
			return
		}
		if len(participants) == 1 {
			existingPortal, err := findExistingPortalByJID(ce.Ctx, ce, login, participants[0])
			if err != nil {
				ce.Reply("Failed to check for existing chat: %v", err)
				return
			}
			if existingPortal != nil && existingPortal.MXID != "" {
				ce.ReplyAdvanced(existingRoomNotice("Chat", participants[0], portalLinkLabel(existingPortal, participants[0]), existingPortal.MXID), false, true)
				return
			}
			if err := client.createDM(ce.Ctx, participants[0]); err != nil {
				ce.Reply("Failed to create chat: %v", err)
				return
			}
			ce.Reply("Create request received for %s. I will send a room link when it is ready.", participants[0])
			go notifyWhenPortalReady(ce, login, participants[0], "Chat")
			return
		}
		groupJID, err := normalizedCheogramGroupJID(participants)
		if err != nil {
			ce.Reply("Failed to create group chat: %v", err)
			return
		}
		existingPortal, err := findExistingPortalByJID(ce.Ctx, ce, login, groupJID)
		if err != nil {
			ce.Reply("Failed to check for existing group chat: %v", err)
			return
		}
		if existingPortal != nil && existingPortal.MXID != "" {
			ce.ReplyAdvanced(existingRoomNotice("Group chat", groupJID, portalLinkLabel(existingPortal, groupJID), existingPortal.MXID), false, true)
			return
		}
		if err := client.createGroupChat(ce.Ctx, name, participants); err != nil {
			ce.Reply("Failed to create group chat: %v", err)
			return
		}
		ce.Reply("Create request received for group chat %s. I will send a room link when it is ready.", groupJID)
		go notifyWhenPortalReady(ce, login, groupJID, "Group chat")
	},
	Name:    "start-chat",
	Aliases: []string{"create"},
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionChats,
		Description: "Create a DM or group chat with phone numbers or JIDs",
	},
	RequiresLogin: true,
}

var CommandListUsers = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		login := ce.User.GetDefaultLogin()
		if login == nil {
			ce.Reply("No login found")
			return
		}
		rows, err := ce.Bridge.DB.UserPortal.GetAllForLogin(ce.Ctx, login.UserLogin)
		if err != nil {
			ce.Reply("Failed to list portals: %v", err)
			return
		}
		users := map[string]bool{}
		for _, row := range rows {
			portal, err := ce.Bridge.GetPortalByKey(ce.Ctx, row.Portal)
			if err != nil || portal == nil {
				continue
			}
			if portal.RoomType != database.RoomTypeDM {
				continue
			}
			jid := string(portal.OtherUserID)
			if jid == "" {
				jid = string(portal.ID)
			}
			jid = UnescapeJID(jid)
			users[jid] = true
		}
		if len(users) == 0 {
			ce.Reply("No users found")
			return
		}
		list := make([]string, 0, len(users))
		for jid := range users {
			list = append(list, jid)
		}
		sort.Strings(list)
		ce.Reply(strings.Join(list, "\n"))
	},
	Name: "list-users",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionChats,
		Description: "List users you have DMs with",
	},
	RequiresLogin: true,
}

var CommandListContacts = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		login := ce.User.GetDefaultLogin()
		if login == nil {
			ce.Reply("No login found")
			return
		}
		client, ok := login.Client.(*MyNetworkClient)
		if !ok || client == nil {
			ce.Reply("Network client not available")
			return
		}
		contacts := client.rosterContactList()
		if len(contacts) == 0 {
			if client.isXMPPConnected() {
				if err := client.requestRoster(); err != nil {
					ce.Reply("No roster contacts loaded and roster refresh failed: %v", err)
					return
				}
				ce.Reply("No roster contacts loaded yet. Requested a roster refresh; retry in a moment.")
				return
			}
			ce.Reply("No roster contacts found")
			return
		}
		ce.Reply(strings.Join(contacts, "\n"))
	},
	Name: "list-contacts",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionChats,
		Description: "List contacts currently present in your XMPP roster",
	},
	RequiresLogin: true,
}

var CommandListContacted = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		login := ce.User.GetDefaultLogin()
		if login == nil {
			ce.Reply("No login found")
			return
		}
		rows, err := ce.Bridge.DB.UserPortal.GetAllForLogin(ce.Ctx, login.UserLogin)
		if err != nil {
			ce.Reply("Failed to list portals: %v", err)
			return
		}
		contacted := map[string]struct{}{}
		for _, row := range rows {
			portal, err := ce.Bridge.GetPortalByKey(ce.Ctx, row.Portal)
			if err != nil || portal == nil {
				jid := UnescapeJID(string(row.Portal.ID))
				if groupContacts := splitCheogramGroupContacts(jid); len(groupContacts) > 0 {
					for _, contact := range groupContacts {
						addContactedJIDs(contacted, contact)
					}
				} else {
					addContactedJIDs(contacted, jid)
				}
				continue
			}
			if !portalUsesGroupChat(portal) {
				jid := string(portal.OtherUserID)
				if jid == "" {
					jid = string(portal.ID)
				}
				addContactedJIDs(contacted, UnescapeJID(jid))
				continue
			}
			for _, contact := range portalContacts(portal) {
				addContactedJIDs(contacted, contact)
			}
		}
		if len(contacted) == 0 {
			ce.Reply("No contacted users found")
			return
		}
		list := make([]string, 0, len(contacted))
		for jid := range contacted {
			list = append(list, jid)
		}
		sort.Strings(list)
		ce.Reply(strings.Join(list, "\n"))
	},
	Name: "list-contacted",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionChats,
		Description: "List users you have contacted directly or via Cheogram group chats",
	},
	RequiresLogin: true,
}

var CommandListUnnamedContacts = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		login := ce.User.GetDefaultLogin()
		if login == nil {
			ce.Reply("No login found")
			return
		}
		type unnamedContact struct {
			JID          string
			Phone        string
			RoomID       id.RoomID
			RemoteName   string
			MessageCount int
			LastTS       time.Time
			Preview      string
		}

		unnamedMap := make(map[string]*unnamedContact)

		userPortals, err := ce.Bridge.DB.UserPortal.GetAllForLogin(ce.Ctx, login.UserLogin)
		if err == nil {
			for _, up := range userPortals {
				portal, err := ce.Bridge.GetExistingPortalByKey(ce.Ctx, up.Portal)
				if err != nil || portal == nil {
					continue
				}
				var jids []string
				if portalUsesGroupChat(portal) {
					jids = portalContacts(portal)
				} else {
					jid := string(portal.OtherUserID)
					if jid == "" {
						jid = string(portal.ID)
					}
					if jid != "" {
						jids = append(jids, UnescapeJID(jid))
					}
				}

				stats := getRoomPruneStats(ce, portal)
				for _, rawJID := range jids {
					jid := bareJID(UnescapeJID(strings.TrimSpace(rawJID)))
					if jid == "" {
						continue
					}
					ghostID := MakeUserID(jid)
					ghost, _ := ce.Bridge.GetExistingGhostByID(ce.Ctx, ghostID)
					var customName, remoteName string
					if ghost != nil {
						if meta, ok := ghost.Metadata.(*GhostMetadata); ok && meta != nil {
							customName = strings.TrimSpace(meta.CustomName)
							remoteName = strings.TrimSpace(meta.RemoteName)
						}
					}
					if customName != "" {
						continue
					}
					existing, exists := unnamedMap[jid]
					if !exists {
						unnamedMap[jid] = &unnamedContact{
							JID:          jid,
							Phone:        extractPhoneDigits(jid),
							RoomID:       portal.MXID,
							RemoteName:   remoteName,
							MessageCount: stats.messageCount,
							LastTS:       stats.lastTS,
							Preview:      stats.preview,
						}
					} else {
						if stats.messageCount > existing.MessageCount {
							existing.MessageCount = stats.messageCount
							existing.LastTS = stats.lastTS
							existing.Preview = stats.preview
							existing.RoomID = portal.MXID
						}
						if remoteName != "" && existing.RemoteName == "" {
							existing.RemoteName = remoteName
						}
					}
				}
			}
		}

		if client, ok := login.Client.(*MyNetworkClient); ok && client != nil {
			roster := client.rosterContactList()
			if meta, ok := login.Metadata.(*LoginMetadata); ok && meta != nil && meta.RemoteUserID != "" {
				roster = append(roster, meta.RemoteUserID)
			}
			for _, rosterJID := range roster {
				jid := bareJID(UnescapeJID(strings.TrimSpace(rosterJID)))
				if jid == "" {
					continue
				}
				ghostID := MakeUserID(jid)
				ghost, _ := ce.Bridge.GetExistingGhostByID(ce.Ctx, ghostID)
				var customName, remoteName string
				if ghost != nil {
					if meta, ok := ghost.Metadata.(*GhostMetadata); ok && meta != nil {
						customName = strings.TrimSpace(meta.CustomName)
						remoteName = strings.TrimSpace(meta.RemoteName)
					}
				}
				if customName != "" {
					continue
				}
				if _, exists := unnamedMap[jid]; !exists {
					unnamedMap[jid] = &unnamedContact{
						JID:        jid,
						Phone:      extractPhoneDigits(jid),
						RemoteName: remoteName,
					}
				}
			}
		}

		if len(unnamedMap) == 0 {
			ce.Reply("All contacts have custom display names set! (0 unnamed contacts found)")
			return
		}

		var list []*unnamedContact
		for _, u := range unnamedMap {
			list = append(list, u)
		}
		sort.Slice(list, func(i, j int) bool {
			if list[i].MessageCount != list[j].MessageCount {
				return list[i].MessageCount > list[j].MessageCount
			}
			if !list[i].LastTS.Equal(list[j].LastTS) {
				return list[i].LastTS.After(list[j].LastTS)
			}
			return list[i].JID < list[j].JID
		})

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Found %d unnamed contact(s) without a custom display name:\n\n", len(list)))
		for _, u := range list {
			label := u.JID
			if u.Phone != "" {
				label = fmt.Sprintf("+%s (%s)", u.Phone, u.JID)
			}
			roomInfo := ""
			if u.RoomID != "" {
				roomInfo = fmt.Sprintf(" (Room: `%s`)", u.RoomID)
			}
			sb.WriteString(fmt.Sprintf("- **%s**%s\n", label, roomInfo))
			remoteInfo := ""
			if u.RemoteName != "" {
				remoteInfo = fmt.Sprintf(" | Remote name: %q", u.RemoteName)
			}
			sb.WriteString(fmt.Sprintf("  - Messages: %d | Last active: %s%s\n", u.MessageCount, formatLastActivity(u.LastTS), remoteInfo))
			if u.Preview != "" {
				sb.WriteString(fmt.Sprintf("  - Last message: %q\n", u.Preview))
			}
		}
		sb.WriteString("\n---\nTo set a custom display name for any contact, run:\n`set-contact-name <jid_or_phone> <name>`\nExample: `set-contact-name +15551234567 Alice Smith`\n\nName your own XMPP identity too, so group names can include you:\n`set-contact-name <your_xmpp_jid_or_phone> <your name>`")
		ce.Reply("%s", sb.String())
	},
	Name:    "list-unnamed-contacts",
	Aliases: []string{"list-unnamed", "unnamed-contacts", "unnamed"},
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionChats,
		Description: "List contacts that do not have a custom display name set (use: list-unnamed-contacts)",
	},
	RequiresLogin: true,
}

var CommandBlockContact = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		handleBlockCommand(ce, false)
	},
	Name: "block",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionChats,
		Description: "Block a contact (use: block [--report] <jid>)",
	},
	RequiresLogin: true,
}

var CommandReportContact = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		handleBlockCommand(ce, true)
	},
	Name: "report",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionChats,
		Description: "Report a contact as spam and block them (use: report <jid>)",
	},
	RequiresLogin: true,
}

var CommandJoinRoom = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		login := ce.User.GetDefaultLogin()
		if login == nil {
			ce.Reply("No login found")
			return
		}
		client, ok := login.Client.(*MyNetworkClient)
		if !ok || client == nil {
			ce.Reply("Network client not available")
			return
		}
		roomJID, full, lookback, err := parseJoinRoomArgs(ce.Args)
		if err != nil {
			ce.Reply("%v", err)
			return
		}
		if ce.Log != nil {
			logJIDNormalization(*ce.Log, "join_room", roomJID)
		}
		portalID := MakePortalID(roomJID)
		if ce.Log != nil {
			ce.Log.Debug().
				Str("raw_jid", roomJID).
				Str("portal_id", string(portalID)).
				Msg("Resolved portal ID for join-room")
		}
		resync := &simplevent.ChatResync{
			EventMeta: simplevent.EventMeta{
				Type:         bridgev2.RemoteEventChatResync,
				PortalKey:    networkid.PortalKey{ID: portalID},
				CreatePortal: true,
				Timestamp:    time.Now(),
			},
			ChatInfo: nil,
		}
		client.bridge.QueueRemoteEvent(login, resync)
		start := time.Now().Add(-lookback)
		if full {
			start = time.Time{}
		}
		client.scheduleMUCBackfill(roomJID, start)
		client.joinMUC(roomJID, "", "")
		ce.Reply("Joining %s and starting backfill", roomJID)
	},
	Name: "join-room",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionChats,
		Description: "Join a room by JID and backfill (use: join-room <room_jid> [full|<duration>]). Example: join-room +15550102100,+15550102200,+15550102300@cheogram.com",
	},
	RequiresLogin: true,
}

var CommandListDiscoveredRooms = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		login := ce.User.GetDefaultLogin()
		if login == nil {
			ce.Reply("No login found")
			return
		}
		client, ok := login.Client.(*MyNetworkClient)
		if !ok || client == nil {
			ce.Reply("Network client not available")
			return
		}
		bookmarks, disco, joined, services := client.discoveredRoomLists()
		all := map[string]bool{}
		for _, jid := range bookmarks {
			all[jid] = true
		}
		for _, jid := range disco {
			all[jid] = true
		}
		for _, jid := range joined {
			all[jid] = true
		}
		allList := make([]string, 0, len(all))
		for jid := range all {
			allList = append(allList, jid)
		}
		sort.Strings(allList)
		format := func(label string, list []string) string {
			if len(list) == 0 {
				return fmt.Sprintf("%s: (none)", label)
			}
			return fmt.Sprintf("%s (%d):\n%s", label, len(list), strings.Join(list, "\n"))
		}
		parts := []string{
			format("Bookmarked rooms", bookmarks),
			format("Discovered rooms", disco),
			format("Joined rooms", joined),
			format("MUC services", services),
			format("All rooms", allList),
		}
		ce.Reply(strings.Join(parts, "\n\n"))
	},
	Name:    "list-discovered-rooms",
	Aliases: []string{"disco-rooms", "discovered-rooms"},
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionChats,
		Description: "List rooms discovered via bookmarks, disco, or joins",
	},
	RequiresLogin: true,
}

func parseListRoomsArgs(args []string) (string, error) {
	filter := "all"
	if len(args) > 1 {
		return "", fmt.Errorf("usage: list-rooms [all|dm|group|orphans|space|stale]")
	}
	if len(args) == 1 {
		filter = strings.ToLower(strings.TrimSpace(args[0]))
	}
	switch filter {
	case "all", "dm", "group", "orphans", "space", "stale":
		return filter, nil
	default:
		return "", fmt.Errorf("unknown room filter %q (use all, dm, group, orphans, space, or stale)", filter)
	}
}

var CommandListRooms = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		login := ce.User.GetDefaultLogin()
		if login == nil {
			ce.Reply("No login found")
			return
		}
		filter, err := parseListRoomsArgs(ce.Args)
		if err != nil {
			ce.Reply("%v", err)
			return
		}
		candidates, err := collectCandidateRooms(ce, login)
		if err != nil {
			ce.Reply("Failed to list rooms: %v", err)
			return
		}
		lines := make([]string, 0, len(candidates)+2)
		for _, room := range candidates {
			kind := "dm"
			if room.portal != nil && room.portal.RoomType != database.RoomTypeDM && room.portal.RoomType != database.RoomTypeSpace {
				kind = "group"
			}
			if (filter == "dm" && kind != "dm") || (filter == "group" && kind != "group") || (filter == "orphans" && !room.isOrphan) || (filter == "stale" && room.messageCount > 0) || filter == "space" {
				continue
			}
			preview := ""
			if room.preview != "" {
				preview = " | " + room.preview
			}
			lines = append(lines, fmt.Sprintf("[%s] %s | %s | messages=%d | last=%s%s", kind, room.roomID, room.name, room.messageCount, formatLastActivity(room.lastTS), preview))
		}
		if filter == "all" || filter == "space" {
			if login.SpaceRoom != "" {
				lines = append(lines, fmt.Sprintf("[space] %s", login.SpaceRoom))
			}
			if filter == "all" && ce.User.ManagementRoom != "" {
				lines = append(lines, fmt.Sprintf("[management] %s", ce.User.ManagementRoom))
			}
		}
		sort.Strings(lines)
		if len(lines) == 0 {
			ce.Reply("No rooms found (filter: %s)", filter)
			return
		}
		ce.Reply("Rooms (filter: %s, %d):\n%s", filter, len(lines), strings.Join(lines, "\n"))
	},
	Name:    "list-rooms",
	Aliases: []string{"rooms", "list-chats"},
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionChats,
		Description: "List all bot-managed Matrix rooms (use: list-rooms [all|dm|group|orphans|space|stale])",
	},
	RequiresLogin: true,
}

var CommandGatewayActions = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		login := ce.User.GetDefaultLogin()
		if login == nil {
			ce.Reply("No login found")
			return
		}
		client, ok := login.Client.(*MyNetworkClient)
		if !ok || client == nil {
			ce.Reply("Network client not available")
			return
		}
		targetJID := "cheogram.com"
		if len(ce.Args) > 0 {
			targetJID = strings.TrimSpace(ce.Args[0])
		}
		result, err := client.discoverGatewayActions(ce.Ctx, targetJID)
		if err != nil {
			ce.Reply("Failed to inspect gateway actions for %s: %v", targetJID, err)
			return
		}
		ce.Reply(formatGatewayActions(result))
	},
	Name:    "gateway-actions",
	Aliases: []string{"service-actions"},
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionChats,
		Description: "List gateway prompts and ad-hoc commands (use: gateway-actions [jid])",
	},
	RequiresLogin: true,
}

var CommandGatewayForm = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		login := ce.User.GetDefaultLogin()
		if login == nil {
			ce.Reply("No login found")
			return
		}
		client, ok := login.Client.(*MyNetworkClient)
		if !ok || client == nil {
			ce.Reply("Network client not available")
			return
		}
		if len(ce.Args) < 2 {
			ce.Reply("Usage: gateway-form <jid> <command node>")
			return
		}
		targetJID := strings.TrimSpace(ce.Args[0])
		node := strings.TrimSpace(strings.Join(ce.Args[1:], " "))
		if node == "" {
			ce.Reply("Usage: gateway-form <jid> <command node>")
			return
		}
		resp, err := client.executeGatewayCommand(ce.Ctx, targetJID, node)
		if err != nil {
			ce.Reply("Failed to execute gateway command %q on %s: %v", node, targetJID, err)
			return
		}
		pending := gatewayPendingFormFromCommand(targetJID, resp)
		if resp.Form == nil || len(pending.Fields) == 0 {
			ce.Reply(formatGatewayCommandCompletion(targetJID, resp))
			return
		}
		sessionKey, err := client.storePendingGatewayForm(ce.Ctx, pending)
		if err != nil {
			ce.Reply("Command returned a form, but saving the pending session failed: %v", err)
			return
		}
		ce.Reply(formatPendingGatewayForm(sessionKey, pending))
	},
	Name: "gateway-form",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionChats,
		Description: "Execute a gateway ad-hoc command and render its form (use: gateway-form <jid> <node>)",
	},
	RequiresLogin: true,
}

var CommandGatewaySubmit = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		login := ce.User.GetDefaultLogin()
		if login == nil {
			ce.Reply("No login found")
			return
		}
		client, ok := login.Client.(*MyNetworkClient)
		if !ok || client == nil {
			ce.Reply("Network client not available")
			return
		}
		if len(ce.Args) < 2 {
			ce.Reply("Usage: gateway-submit <session> field=value [field=value ...]")
			return
		}
		sessionKey := strings.TrimSpace(ce.Args[0])
		pending, ok := client.getPendingGatewayForm(sessionKey)
		if !ok {
			ce.Reply("No pending gateway form found for session %s", sessionKey)
			return
		}
		assignmentsRaw := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ce.RawArgs), sessionKey))
		values, err := parseGatewayAssignments(assignmentsRaw)
		if err != nil {
			ce.Reply("%v", err)
			return
		}
		if err := validateGatewayAssignments(pending, values); err != nil {
			ce.Reply("%v", err)
			return
		}
		resp, err := client.submitGatewayCommand(ce.Ctx, pending, values)
		if err != nil {
			ce.Reply("Failed to submit gateway form %s: %v", sessionKey, err)
			return
		}
		nextPending := gatewayPendingFormFromCommand(pending.TargetJID, resp)
		if resp.Form == nil || len(nextPending.Fields) == 0 || resp.Status == "completed" {
			_ = client.deletePendingGatewayForm(ce.Ctx, sessionKey)
			ce.Reply(formatGatewayCommandCompletion(pending.TargetJID, resp))
			return
		}
		nextPending.CreatedAt = pending.CreatedAt
		if err := client.replacePendingGatewayForm(ce.Ctx, sessionKey, nextPending); err != nil {
			ce.Reply("Gateway form advanced, but saving the updated session failed: %v", err)
			return
		}
		ce.Reply(formatPendingGatewayForm(sessionKey, nextPending))
	},
	Name: "gateway-submit",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionChats,
		Description: "Submit a pending gateway form (use: gateway-submit <session> field=value ...)",
	},
	RequiresLogin: true,
}

var CommandListLogins = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		verbose := false
		if len(ce.Args) > 1 {
			ce.Reply("Usage: list-logins [--verbose]")
			return
		}
		if len(ce.Args) == 1 {
			switch ce.Args[0] {
			case "--verbose", "-v":
				verbose = true
			default:
				ce.Reply("Usage: list-logins [--verbose]")
				return
			}
		}
		logins := ce.User.GetUserLogins()
		if len(logins) == 0 {
			ce.Reply("No logins found")
			return
		}
		entries := make([]string, 0, len(logins))
		for _, login := range logins {
			if login == nil {
				continue
			}
			state := "unknown"
			timestamp := "unknown"
			errorCode := ""
			errorMessage := ""
			if login.BridgeState != nil {
				prev := login.BridgeState.GetPrev()
				state = string(prev.StateEvent)
				if !prev.Timestamp.Time.IsZero() {
					timestamp = prev.Timestamp.Time.Local().Format(time.RFC3339)
				}
				errorCode = string(prev.Error)
				errorMessage = strings.TrimSpace(prev.Message)
			}
			remote := strings.TrimSpace(login.RemoteName)
			if remote == "" {
				remote = "(unknown)"
			}
			entry := fmt.Sprintf("%s - %s (%s)", login.ID, remote, state)
			if verbose {
				entry = fmt.Sprintf("%s at %s", entry, timestamp)
				if errorCode != "" {
					entry = fmt.Sprintf("%s error=%s", entry, errorCode)
					if errorMessage != "" {
						entry = fmt.Sprintf("%s msg=%q", entry, errorMessage)
					}
				}
			}
			entries = append(entries, entry)
		}
		sort.Strings(entries)
		ce.Reply(strings.Join(entries, "\n"))
	},
	Name: "list-logins",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionAdmin,
		Description: "List login IDs for this Matrix user (use: list-logins --verbose)",
	},
	RequiresLogin: true,
}

var CommandSetContactName = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		if len(ce.Args) < 2 {
			ce.Reply("Usage: set-contact-name <phone or JID> <display name>\nUsage: set-contact-name <phone or JID> --clear")
			return
		}
		jid := normalizeJID(ce.Args[0])
		if jid == "" {
			ce.Reply("Invalid JID")
			return
		}
		ghostID := MakeUserID(jid)
		ghost, err := ce.Bridge.GetGhostByID(ce.Ctx, ghostID)
		if err != nil {
			ce.Reply("Failed to load ghost: %v", err)
			return
		}
		meta, _ := ghost.Metadata.(*GhostMetadata)
		if meta == nil {
			meta = &GhostMetadata{}
		}
		login := ce.User.GetDefaultLogin()
		suffix := defaultContactNameSuffix
		if login != nil {
			if client, ok := login.Client.(*MyNetworkClient); ok && client != nil {
				suffix = client.contactNameSuffix()
			}
		}

		if ce.Args[1] == "--clear" {
			if strings.TrimSpace(meta.CustomName) == "" {
				ce.Reply("No custom display name is set for %s", jid)
				return
			}
			meta.CustomName = ""
			ghost.Metadata = meta
			fallback := contactDisplayName(ce.Ctx, ce.Bridge, jid)
			if fallback == "" {
				fallback = contactLabelFromJID(jid)
			}
			if fallback != "" {
				ghost.UpdateInfo(ce.Ctx, &bridgev2.UserInfo{Name: &fallback})
			}
			if err := ce.Bridge.DB.Ghost.Update(ce.Ctx, ghost.Ghost); err != nil {
				ce.Reply("Failed to clear display name for %s: %v", jid, err)
				return
			}
			if login != nil {
				if err := refreshContactNamedPortals(ce.Ctx, ce.Bridge, login, jid, suffix); err != nil {
					ce.Reply("Cleared display name for %s, but room refresh failed: %v", jid, err)
					return
				}
			}
			if fallback == "" || fallback == jid {
				ce.Reply("Cleared custom display name for %s", jid)
			} else {
				ce.Reply("Cleared custom display name for %s (now using %s)", jid, fallback)
			}
			return
		}

		name := normalizeContactName(strings.Join(ce.Args[1:], " "))
		if name == "" {
			ce.Reply("Display name cannot be empty")
			return
		}
		meta.CustomName = name
		ghost.Metadata = meta
		ghost.UpdateInfo(ce.Ctx, &bridgev2.UserInfo{Name: &name})
		if err := ce.Bridge.DB.Ghost.Update(ce.Ctx, ghost.Ghost); err != nil {
			ce.Reply("Failed to save display name for %s: %v", jid, err)
			return
		}
		if login != nil {
			if err := refreshContactNamedPortals(ce.Ctx, ce.Bridge, login, jid, suffix); err != nil {
				ce.Reply("Set display name for %s, but room refresh failed: %v", jid, err)
				return
			}
		}
		ce.Reply("Set display name for %s", jid)
	},
	Name:    "set-contact-name",
	Aliases: []string{"set-name"},
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionChats,
		Description: "Set a custom display name for a user (--clear removes it)",
	},
	RequiresLogin: true,
}

var CommandAutoRename = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		login := ce.User.GetDefaultLogin()
		if login == nil {
			ce.Reply("No login found")
			return
		}
		client, ok := login.Client.(*MyNetworkClient)
		suffix := defaultContactNameSuffix
		if ok && client != nil {
			suffix = client.contactNameSuffix()
		}
		count, err := refreshAutoNamedPortals(ce.Ctx, ce.Bridge, login, suffix)
		if err != nil {
			ce.Reply("Failed to re-run auto rename: %v", err)
			return
		}
		ce.Reply("Queued auto rename for %d rooms", count)
	},
	Name: "auto-rename",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionChats,
		Description: "Re-run auto naming for all auto-named rooms",
	},
	RequiresLogin: true,
}

var CommandDeleteAllPortals = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		if len(ce.Args) == 0 || (ce.Args[0] != "yes" && ce.Args[0] != "--yes") {
			ce.Reply("This will remove all bridged rooms, the space, and log you out. Run: delete-all-portals yes")
			return
		}
		login := ce.User.GetDefaultLogin()
		if login == nil {
			ce.Reply("No login found")
			return
		}
		if err := deleteAllPortals(ce, login); err != nil {
			ce.Reply("%v", err)
			return
		}
		ce.Reply("Deleted all bridged rooms and the space. Management room kept. You can log in again with !login.")
	},
	Name: "delete-all-portals",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionAdmin,
		Description: "Remove all bridged rooms, delete the space, and log out (use: delete-all-portals yes)",
	},
	RequiresLogin: true,
}

var CommandLogout = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		loginID, purge, err := parseLogoutArgs(ce.Args)
		if err != nil {
			ce.Reply("%v", err)
			return
		}
		login, err := resolveLoginForCommand(ce, loginID)
		if err != nil {
			ce.Reply("%v", err)
			return
		}
		if purge {
			if err := deleteAllPortals(ce, login); err != nil {
				ce.Reply("%v", err)
				return
			}
			ce.Reply("Deleted all bridged rooms and the space. Management room kept. You can log in again with !login.")
			return
		}
		if login.Client != nil {
			login.Client.LogoutRemote(ce.Ctx)
			login.Client.Disconnect()
		}
		if login.BridgeState != nil {
			login.BridgeState.Send(status.BridgeState{StateEvent: status.StateLoggedOut})
		}
		ce.Reply("Logged out without removing rooms. Use `logout purge` to delete rooms and the space.")
	},
	Name: "logout",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionAdmin,
		Description: "Log out without removing rooms (use: logout [login_id] [purge])",
	},
	RequiresLogin: true,
}

func normalizeJID(input string) string {
	input = strings.TrimSpace(UnescapeJID(input))
	if input == "" || (!strings.Contains(input, "@") && !isPhoneLike(input)) {
		return ""
	}
	return CanonicalBareJID(input)
}

func splitCheogramGroupContacts(jid string) []string {
	jid = CanonicalBareJID(jid)
	if jid == "" || !strings.Contains(jid, ",") || !strings.HasSuffix(jid, "@cheogram.com") {
		return nil
	}
	localpart := strings.TrimSuffix(jid, "@cheogram.com")
	parts := strings.Split(localpart, ",")
	contacts := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		contacts = append(contacts, canonicalCheogramLocalPart(part)+"@cheogram.com")
	}
	return contacts
}

func addContactedJIDs(dst map[string]struct{}, jid string) {
	jid = bareJID(strings.TrimSpace(jid))
	if jid == "" {
		return
	}
	if strings.Contains(jid, "/") || strings.Contains(jid, ",") {
		return
	}
	dst[jid] = struct{}{}
}

func parseBlockCommandArgs(args []string, defaultReport bool) (string, bool, error) {
	report := defaultReport
	filtered := make([]string, 0, len(args))
	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		if arg == "" {
			continue
		}
		if arg == "--report" {
			report = true
			continue
		}
		filtered = append(filtered, arg)
	}
	if len(filtered) != 1 {
		return "", false, fmt.Errorf("Usage: block [--report] <jid>")
	}
	jid := normalizeJID(filtered[0])
	if jid == "" {
		return "", false, fmt.Errorf("Usage: block [--report] <jid>")
	}
	return jid, report, nil
}

func handleBlockCommand(ce *commands.Event, defaultReport bool) {
	login := ce.User.GetDefaultLogin()
	if login == nil {
		ce.Reply("No login found")
		return
	}
	client, ok := login.Client.(*MyNetworkClient)
	if !ok || client == nil {
		ce.Reply("Network client not available")
		return
	}
	jid, report, err := parseBlockCommandArgs(ce.Args, defaultReport)
	if err != nil {
		ce.Reply("%v", err)
		return
	}
	serverMsgID := ""
	if report {
		if portal, err := findExistingPortalByJID(ce.Ctx, ce, login, jid); err == nil && portal != nil {
			if meta, ok := portal.Metadata.(*PortalMetadata); ok && meta != nil {
				serverMsgID = meta.LastServerMessageID
			}
		} else if err != nil {
			if ce.Log != nil {
				ce.Log.Debug().Err(err).Str("jid", jid).Msg("Failed to look up portal for spam report stanza-id")
			}
		}
	}
	if err := client.blockContact(ce.Ctx, jid, report, serverMsgID); err != nil {
		ce.Reply("Failed to block %s: %v", jid, err)
		return
	}
	if report {
		ce.Reply("Blocked and reported %s", jid)
		return
	}
	ce.Reply("Blocked %s", jid)
}

func findExistingPortalByJID(ctx context.Context, ce *commands.Event, login *bridgev2.UserLogin, jid string) (*bridgev2.Portal, error) {
	portalKey := networkid.PortalKey{ID: MakePortalID(jid)}
	portal, err := ce.Bridge.GetExistingPortalByKey(ctx, portalKey)
	if err != nil {
		return nil, err
	}
	if portal != nil {
		return portal, nil
	}
	userPortals, err := ce.Bridge.DB.UserPortal.GetAllForLogin(ctx, login.UserLogin)
	if err != nil {
		return nil, err
	}
	for _, userPortal := range userPortals {
		if userPortal.Portal != portalKey {
			continue
		}
		portal, err = ce.Bridge.GetPortalByKey(ctx, userPortal.Portal)
		if err != nil {
			return nil, err
		}
		if portal != nil {
			return portal, nil
		}
	}
	return nil, nil
}

func existingRoomNotice(kind, jid, label string, roomID id.RoomID) string {
	roomURL := roomID.URI().MatrixToURL()
	return fmt.Sprintf("%s for <code>%s</code> already exists: <a href=\"%s\">Open %s</a>", kind, html.EscapeString(jid), html.EscapeString(roomURL), html.EscapeString(roomLinkLabel(label, roomID)))
}

func createdRoomNotice(kind, jid, label string, roomID id.RoomID) string {
	roomURL := roomID.URI().MatrixToURL()
	return fmt.Sprintf("%s for <code>%s</code> is ready: <a href=\"%s\">Open %s</a>", kind, html.EscapeString(jid), html.EscapeString(roomURL), html.EscapeString(roomLinkLabel(label, roomID)))
}

func notifyWhenPortalReady(ce *commands.Event, login *bridgev2.UserLogin, jid, kind string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		portal, err := findExistingPortalByJID(ctx, ce, login, jid)
		if err == nil && portal != nil && portal.MXID != "" {
			ce.ReplyAdvanced(createdRoomNotice(kind, jid, portalLinkLabel(portal, jid), portal.MXID), false, true)
			return
		}
		select {
		case <-ctx.Done():
			ce.Reply("Creating %s for %s timed out. If the room was not created, please try again or check logs.", kind, jid)
			return
		case <-ticker.C:
		}
	}
}

func portalLinkLabel(portal *bridgev2.Portal, fallback string) string {
	if portal == nil {
		return fallback
	}
	if meta, ok := portal.Metadata.(*PortalMetadata); ok && meta != nil {
		if name := strings.TrimSpace(meta.InitialName); name != "" {
			return name
		}
	}
	if name := strings.TrimSpace(portal.Name); name != "" {
		return name
	}
	return fallback
}

func roomLinkLabel(label string, roomID id.RoomID) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return fmt.Sprintf("room %s", roomID)
	}
	return fmt.Sprintf("room %s", label)
}

func sendNotice(bot bridgev2.MatrixAPI, roomID id.RoomID, msg string) {
	if bot == nil || roomID == "" || msg == "" {
		return
	}
	content := &event.MessageEventContent{
		MsgType: event.MsgNotice,
		Body:    msg,
	}
	_, _ = bot.SendMessage(context.Background(), roomID, event.EventMessage, &event.Content{Parsed: content}, nil)
}

func parseSyncArgs(args []string) (bool, time.Duration, error) {
	if len(args) == 0 {
		return false, defaultMAMLookback, nil
	}
	if len(args) > 1 {
		return false, 0, fmt.Errorf("Usage: sync [full|<duration>] (e.g. sync 30d, sync full)")
	}
	arg := strings.ToLower(strings.TrimSpace(args[0]))
	if arg == "full" {
		return true, 0, nil
	}
	lookback, err := parseLookbackDuration(arg)
	if err != nil {
		return false, 0, fmt.Errorf("Invalid duration %q (use 7d, 30d, 12h, or full)", arg)
	}
	return false, lookback, nil
}

func parseLookbackDuration(arg string) (time.Duration, error) {
	if strings.HasSuffix(arg, "d") {
		num := strings.TrimSuffix(arg, "d")
		if num == "" {
			return 0, fmt.Errorf("empty duration")
		}
		days, err := strconv.Atoi(num)
		if err != nil || days <= 0 {
			return 0, fmt.Errorf("invalid day value")
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	return time.ParseDuration(arg)
}

func parseJoinRoomArgs(args []string) (string, bool, time.Duration, error) {
	if len(args) == 0 {
		return "", false, 0, fmt.Errorf("Usage: join-room <room_jid> [full|<duration>]")
	}
	roomJID := strings.TrimSpace(args[0])
	if roomJID == "" || !strings.Contains(roomJID, "@") {
		return "", false, 0, fmt.Errorf("Invalid room JID")
	}
	if strings.Contains(roomJID, " ") {
		return "", false, 0, fmt.Errorf("Invalid room JID (contains spaces)")
	}
	if len(args) == 1 {
		return roomJID, false, defaultMAMLookback, nil
	}
	if len(args) > 2 {
		return "", false, 0, fmt.Errorf("Usage: join-room <room_jid> [full|<duration>]")
	}
	arg := strings.ToLower(strings.TrimSpace(args[1]))
	if arg == "full" {
		return roomJID, true, 0, nil
	}
	lookback, err := parseLookbackDuration(arg)
	if err != nil {
		return "", false, 0, fmt.Errorf("Invalid duration %q (use 7d, 30d, 12h, or full)", arg)
	}
	return roomJID, false, lookback, nil
}

func parseLogoutArgs(args []string) (string, bool, error) {
	if len(args) == 0 {
		return "", false, nil
	}
	if len(args) > 2 {
		return "", false, fmt.Errorf("Usage: logout [login_id] [purge]")
	}
	if strings.EqualFold(args[0], "purge") {
		if len(args) > 1 {
			return "", false, fmt.Errorf("Usage: logout [login_id] [purge]")
		}
		return "", true, nil
	}
	loginID := strings.TrimSpace(args[0])
	if loginID == "" {
		return "", false, fmt.Errorf("Usage: logout [login_id] [purge]")
	}
	if len(args) == 2 {
		if !strings.EqualFold(args[1], "purge") {
			return "", false, fmt.Errorf("Usage: logout [login_id] [purge]")
		}
		return loginID, true, nil
	}
	return loginID, false, nil
}

func resolveLoginForCommand(ce *commands.Event, loginID string) (*bridgev2.UserLogin, error) {
	if loginID == "" {
		logins := ce.User.GetUserLoginIDs()
		if len(logins) == 0 {
			return nil, fmt.Errorf("No login found")
		}
		if len(logins) > 1 {
			return nil, fmt.Errorf("Multiple logins found. Use: logout <login_id> [purge]")
		}
		login := ce.User.GetDefaultLogin()
		if login == nil {
			return nil, fmt.Errorf("No login found")
		}
		return login, nil
	}
	login, err := ce.Bridge.GetExistingUserLoginByID(ce.Ctx, networkid.UserLoginID(loginID))
	if err != nil {
		return nil, fmt.Errorf("Failed to load login %q: %w", loginID, err)
	}
	if login == nil || login.User == nil || login.User.MXID != ce.User.MXID {
		return nil, fmt.Errorf("Login %q not found", loginID)
	}
	return login, nil
}

func formatGatewayActions(result *GatewayActionDiscovery) string {
	if result == nil {
		return "No gateway actions found"
	}
	parts := []string{fmt.Sprintf("Gateway target: %s", result.TargetJID)}
	if result.GatewayDescription != "" {
		parts = append(parts, fmt.Sprintf("Description: %s", result.GatewayDescription))
	}
	parts = append(parts, fmt.Sprintf("Supports ad-hoc commands: %t", result.SupportsCommands))
	parts = append(parts, fmt.Sprintf("Supports gateway IQ: %t", result.SupportsGatewayIQ))
	if result.GatewayPrompt != "" {
		parts = append(parts, fmt.Sprintf("Gateway prompt: %s", result.GatewayPrompt))
	}
	if result.GatewayPromptError != "" {
		parts = append(parts, fmt.Sprintf("Gateway prompt error: %s", result.GatewayPromptError))
	}
	if result.CommandListError != "" {
		parts = append(parts, fmt.Sprintf("Command list error: %s", result.CommandListError))
	}
	if len(result.Commands) == 0 {
		parts = append(parts, "Commands: (none advertised)")
		return strings.Join(parts, "\n")
	}
	lines := make([]string, 0, len(result.Commands))
	for _, item := range result.Commands {
		label := firstNonEmpty(item.Name, item.Node, item.JID)
		if item.Node != "" && item.Name != "" && item.Name != item.Node {
			label = fmt.Sprintf("%s (node: %s)", item.Name, item.Node)
		}
		lines = append(lines, "- "+label)
	}
	parts = append(parts, "Commands:\n"+strings.Join(lines, "\n"))
	parts = append(parts, fmt.Sprintf("Run `gateway-form %s <node>` to execute one.", result.TargetJID))
	return strings.Join(parts, "\n")
}

func formatPendingGatewayForm(sessionKey string, pending PendingGatewayForm) string {
	parts := []string{
		fmt.Sprintf("Gateway form session: %s", sessionKey),
		fmt.Sprintf("Target: %s", pending.TargetJID),
		fmt.Sprintf("Command node: %s", pending.Node),
	}
	if pending.Title != "" {
		parts = append(parts, fmt.Sprintf("Title: %s", pending.Title))
	}
	for _, note := range pending.Notes {
		parts = append(parts, fmt.Sprintf("Note: %s", note))
	}
	for _, instruction := range pending.Instructions {
		parts = append(parts, fmt.Sprintf("Instruction: %s", instruction))
	}
	fieldLines := make([]string, 0, len(pending.Fields))
	for _, field := range pending.Fields {
		if field.Var == "" {
			continue
		}
		line := field.Var
		if field.Label != "" {
			line = fmt.Sprintf("%s - %s", line, field.Label)
		}
		if field.Type != "" {
			line = fmt.Sprintf("%s [%s]", line, field.Type)
		}
		if field.Required {
			line += " required"
		}
		if field.Description != "" {
			line = fmt.Sprintf("%s - %s", line, field.Description)
		}
		if len(field.Options) > 0 {
			options := make([]string, 0, len(field.Options))
			for _, option := range field.Options {
				options = append(options, firstNonEmpty(option.Label, option.Value))
			}
			line = fmt.Sprintf("%s (options: %s)", line, strings.Join(options, ", "))
		}
		fieldLines = append(fieldLines, "- "+line)
	}
	if len(fieldLines) == 0 {
		parts = append(parts, "Fields: (none)")
	} else {
		parts = append(parts, "Fields:\n"+strings.Join(fieldLines, "\n"))
	}
	parts = append(parts, fmt.Sprintf("Submit with `gateway-submit %s field=value ...`.", sessionKey))
	return strings.Join(parts, "\n")
}

func formatGatewayCommandCompletion(targetJID string, resp *AdHocCommand) string {
	parts := []string{fmt.Sprintf("Gateway command finished for %s.", targetJID)}
	if resp != nil {
		if resp.Node != "" {
			parts = append(parts, fmt.Sprintf("Command node: %s", resp.Node))
		}
		if resp.Status != "" {
			parts = append(parts, fmt.Sprintf("Status: %s", resp.Status))
		}
		for _, note := range resp.Notes {
			text := strings.TrimSpace(note.Text)
			if text != "" {
				parts = append(parts, fmt.Sprintf("Note: %s", text))
			}
		}
	}
	return strings.Join(parts, "\n")
}

func parseGatewayAssignments(raw string) (map[string]string, error) {
	tokens, err := splitGatewayTokens(raw)
	if err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		return nil, fmt.Errorf("Usage: gateway-submit <session> field=value [field=value ...]")
	}
	values := make(map[string]string, len(tokens))
	for _, token := range tokens {
		parts := strings.SplitN(token, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("Invalid field assignment %q (expected field=value)", token)
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if key == "" {
			return nil, fmt.Errorf("Invalid empty field name in %q", token)
		}
		values[key] = value
	}
	return values, nil
}

func splitGatewayTokens(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	tokens := make([]string, 0, 4)
	var current strings.Builder
	var quote rune
	escaped := false
	flush := func() {
		if current.Len() == 0 {
			return
		}
		tokens = append(tokens, current.String())
		current.Reset()
	}
	for _, r := range raw {
		switch {
		case escaped:
			current.WriteRune(r)
			escaped = false
		case r == '\\':
			escaped = true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				current.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
		case r == ' ' || r == '\t' || r == '\n':
			flush()
		default:
			current.WriteRune(r)
		}
	}
	if escaped {
		return nil, fmt.Errorf("Trailing escape in gateway-submit arguments")
	}
	if quote != 0 {
		return nil, fmt.Errorf("Unterminated quote in gateway-submit arguments")
	}
	flush()
	return tokens, nil
}

func validateGatewayAssignments(pending PendingGatewayForm, values map[string]string) error {
	known := make(map[string]PendingGatewayField, len(pending.Fields))
	for _, field := range pending.Fields {
		if field.Var == "" {
			continue
		}
		known[field.Var] = field
	}
	for key := range values {
		field, ok := known[key]
		if !ok {
			return fmt.Errorf("Field %q is not part of pending session %s", key, pending.Node)
		}
		if len(field.Options) > 0 {
			matched := false
			for _, option := range field.Options {
				if values[key] == option.Value || values[key] == option.Label {
					matched = true
					values[key] = option.Value
					break
				}
			}
			if !matched {
				choices := make([]string, 0, len(field.Options))
				for _, option := range field.Options {
					choices = append(choices, firstNonEmpty(option.Label, option.Value))
				}
				return fmt.Errorf("Field %q must be one of: %s", key, strings.Join(choices, ", "))
			}
		}
	}
	for _, field := range pending.Fields {
		if !field.Required || field.Var == "" || field.Type == "hidden" {
			continue
		}
		if _, ok := values[field.Var]; !ok && len(field.Values) == 0 {
			return fmt.Errorf("Missing required field %q", field.Var)
		}
	}
	return nil
}

func removeRoomFromSpace(ctx context.Context, bot bridgev2.MatrixAPI, spaceRoom id.RoomID, childRoom id.RoomID) error {
	if bot == nil || spaceRoom == "" || childRoom == "" || spaceRoom == childRoom {
		return nil
	}
	if err := bot.EnsureJoined(ctx, spaceRoom); err != nil {
		return fmt.Errorf("ensure joined Space %s: %w", spaceRoom, err)
	}
	if _, err := bot.SendState(ctx, spaceRoom, event.StateSpaceChild, childRoom.String(), &event.Content{
		Parsed: &event.SpaceChildEventContent{
			Via: nil,
		},
	}, time.Now()); err != nil {
		return fmt.Errorf("remove %s from Space %s: %w", childRoom, spaceRoom, err)
	}
	if _, err := bot.SendState(ctx, childRoom, event.StateSpaceParent, spaceRoom.String(), &event.Content{
		Parsed: &event.SpaceParentEventContent{Via: nil, Canonical: false},
	}, time.Now()); err != nil {
		return fmt.Errorf("clear Space parent on %s: %w", childRoom, err)
	}
	return nil
}

func addRoomToSpace(ctx context.Context, bridge *bridgev2.Bridge, spaceRoom id.RoomID, childRoom id.RoomID, canonical bool) error {
	if bridge == nil || bridge.Bot == nil || spaceRoom == "" || childRoom == "" || spaceRoom == childRoom {
		return nil
	}
	via := []string{bridge.Matrix.ServerName()}
	_, err := bridge.Bot.SendState(ctx, spaceRoom, event.StateSpaceChild, childRoom.String(), &event.Content{
		Parsed: &event.SpaceChildEventContent{
			Via: via,
		},
	}, time.Now())
	if err != nil {
		return err
	}
	if canonical {
		_, _ = bridge.Bot.SendState(ctx, childRoom, event.StateSpaceParent, spaceRoom.String(), &event.Content{
			Parsed: &event.SpaceParentEventContent{
				Via:       via,
				Canonical: true,
			},
		}, time.Now())
	}
	return nil
}

func deleteSinglePortal(ce *commands.Event, portal *bridgev2.Portal) error {
	if portal == nil {
		return nil
	}
	oldMXID := portal.MXID
	login := ce.User.GetDefaultLogin()
	var cleanupErr error
	if login != nil && login.SpaceRoom != "" && oldMXID != "" {
		cleanupErr = removeRoomFromSpace(ce.Ctx, ce.Bridge.Bot, login.SpaceRoom, oldMXID)
	}
	userPortals, err := ce.Bridge.DB.UserPortal.GetAllInPortal(ce.Ctx, portal.PortalKey)
	if err != nil {
		return fmt.Errorf("failed to list portal users: %w", err)
	}
	for _, up := range userPortals {
		if err := ce.Bridge.DB.UserPortal.Delete(ce.Ctx, up); err != nil {
			return fmt.Errorf("failed to delete portal user: %w", err)
		}
	}
	err = portal.Delete(ce.Ctx)
	if err != nil {
		return fmt.Errorf("failed to delete portal: %w", err)
	}
	if oldMXID != "" {
		if err := ce.Bridge.Bot.DeleteRoom(ce.Ctx, oldMXID, false); err != nil {
			return fmt.Errorf("failed to purge Matrix room %s: %w", oldMXID, err)
		}
	}
	if cleanupErr != nil {
		return cleanupErr
	}
	return nil
}

type roomPruneStats struct {
	portal       *bridgev2.Portal
	targetJID    string
	name         string
	messageCount int
	lastTS       time.Time
	preview      string
}

func getRoomPruneStats(ce *commands.Event, portal *bridgev2.Portal) *roomPruneStats {
	stats := &roomPruneStats{
		portal:    portal,
		targetJID: UnescapeJID(string(portal.ID)),
		name:      portal.Name,
	}
	if stats.name == "" {
		stats.name = stats.targetJID
	}
	_ = ce.Bridge.DB.Message.GetDB().QueryRow(ce.Ctx,
		"SELECT COUNT(*) FROM message WHERE bridge_id=$1 AND room_id=$2 AND room_receiver=$3 AND mxid NOT LIKE '~fake:%'",
		portal.BridgeID, portal.ID, portal.Receiver).Scan(&stats.messageCount)

	lastMsg, err := ce.Bridge.DB.Message.GetLastNonFakePartAtOrBeforeTime(ce.Ctx, portal.PortalKey, time.Now())
	if err == nil && lastMsg != nil {
		stats.lastTS = lastMsg.Timestamp
		if portal.MXID != "" && lastMsg.MXID != "" {
			evt, err := ce.Bridge.Bot.GetEvent(ce.Ctx, portal.MXID, lastMsg.MXID)
			if err == nil && evt != nil && evt.Content.AsMessage() != nil {
				stats.preview = strings.TrimSpace(evt.Content.AsMessage().Body)
				if len(stats.preview) > 50 {
					stats.preview = stats.preview[:47] + "..."
				}
			}
		}
	}
	return stats
}

func formatLastActivity(ts time.Time) string {
	if ts.IsZero() {
		return "Never"
	}
	days := int(time.Since(ts).Hours() / 24)
	if days == 0 {
		return fmt.Sprintf("Today (%s)", ts.UTC().Format("2006-01-02 15:04 UTC"))
	} else if days == 1 {
		return fmt.Sprintf("Yesterday (%s)", ts.UTC().Format("2006-01-02 15:04 UTC"))
	}
	return fmt.Sprintf("%d days ago (%s)", days, ts.UTC().Format("2006-01-02 15:04 UTC"))
}

func parsePruneRoomsArgs(args []string) (confirm bool, emptyOnly bool, days int, err error) {
	days = 30
	for _, arg := range args {
		arg = strings.TrimSpace(strings.ToLower(arg))
		if arg == "" {
			continue
		}
		if arg == "confirm" || arg == "yes" || arg == "--yes" {
			confirm = true
		} else if arg == "dry-run" || arg == "--dry-run" {
			confirm = false
		} else if arg == "empty" || arg == "--empty" || arg == "empty-only" || arg == "--empty-only" || arg == "-e" {
			emptyOnly = true
		} else if d, parseErr := strconv.Atoi(arg); parseErr == nil {
			if d < 0 {
				return false, false, 0, fmt.Errorf("days threshold must be greater than or equal to 0, got %d", d)
			}
			days = d
			if days == 0 {
				emptyOnly = true
			}
		} else {
			return false, false, 0, fmt.Errorf("unknown argument %q (usage: prune-rooms [empty] [dry-run|confirm] [days])", arg)
		}
	}
	return confirm, emptyOnly, days, nil
}

func parsePruneDuplicatesArgs(args []string) (confirm bool, err error) {
	for _, arg := range args {
		arg = strings.TrimSpace(strings.ToLower(arg))
		if arg == "" {
			continue
		}
		if arg == "confirm" || arg == "yes" || arg == "--yes" {
			confirm = true
		} else if arg == "dry-run" || arg == "--dry-run" {
			confirm = false
		} else {
			return false, fmt.Errorf("unknown argument %q (usage: prune-duplicates [dry-run|confirm])", arg)
		}
	}
	return confirm, nil
}

func stripContactSuffixAny(s string) string {
	s = strings.TrimSpace(s)
	for {
		lower := strings.ToLower(s)
		stripped := false
		for _, suffix := range []string{
			" (jmp.chat)", " (jmpchat)", " (jmp)", " (cheogram)", " (jmp.chat sms)", " (sms)",
			" [jmp.chat]", " [jmpchat]", " [jmp]", " [cheogram]", " [jmp.chat sms]", " [sms]",
		} {
			if strings.HasSuffix(lower, suffix) {
				s = strings.TrimSpace(s[:len(s)-len(suffix)])
				stripped = true
				break
			}
		}
		if !stripped {
			break
		}
	}
	if idx := strings.LastIndex(s, " ("); idx != -1 && strings.HasSuffix(s, ")") {
		tag := s[idx+2 : len(s)-1]
		if len(tag) <= 15 && !strings.ContainsAny(tag, "0123456789") {
			s = strings.TrimSpace(s[:idx])
		}
	} else if idx := strings.LastIndex(s, " ["); idx != -1 && strings.HasSuffix(s, "]") {
		tag := s[idx+2 : len(s)-1]
		if len(tag) <= 15 && !strings.ContainsAny(tag, "0123456789") {
			s = strings.TrimSpace(s[:idx])
		}
	}
	return s
}

func extractPhoneDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	digits := b.String()
	if len(digits) == 11 && digits[0] == '1' {
		return digits[1:]
	}
	if len(digits) == 10 {
		return digits
	}
	if len(digits) >= 7 {
		return digits
	}
	return ""
}

type candidateRoom struct {
	roomID       id.RoomID
	portal       *bridgev2.Portal
	targetJID    string
	name         string
	messageCount int
	lastTS       time.Time
	preview      string
	isOrphan     bool
	matchKeys    map[string]bool
}

func computeRoomMatchKeys(ctx context.Context, bridge *bridgev2.Bridge, targetJID, ghostJID, roomName string) map[string]bool {
	keys := make(map[string]bool)

	addKey := func(prefix, val string) {
		val = strings.TrimSpace(val)
		if val == "" {
			return
		}
		keys[prefix+":"+strings.ToLower(val)] = true
	}

	for _, rawJID := range []string{targetJID, ghostJID} {
		if rawJID == "" {
			continue
		}
		bare := bareJID(UnescapeJID(rawJID))
		addKey("jid", bare)
		if digits := extractPhoneDigits(bare); digits != "" {
			addKey("phone", digits)
		}
	}

	if bridge != nil {
		lookupJID := targetJID
		if lookupJID == "" {
			lookupJID = ghostJID
		}
		if lookupJID != "" {
			ghost, err := bridge.GetExistingGhostByID(ctx, MakeUserID(lookupJID))
			if err == nil && ghost != nil {
				if meta, ok := ghost.Metadata.(*GhostMetadata); ok && meta != nil {
					if meta.CustomName != "" {
						addKey("name", meta.CustomName)
					}
					if meta.RemoteName != "" {
						addKey("name", meta.RemoteName)
					}
				}
				if ghost.Name != "" {
					addKey("name", stripContactSuffixAny(ghost.Name))
				}
			}
		}
	}

	if roomName != "" {
		clean := stripContactSuffixAny(roomName)
		lower := strings.ToLower(clean)
		if len(clean) >= 2 && lower != "jmpchat group" && lower != "(unnamed space)" && !strings.HasPrefix(lower, "unnamed") {
			addKey("name", clean)
		}
		if digits := extractPhoneDigits(roomName); digits != "" {
			addKey("phone", digits)
		}
	}

	return keys
}

func collectCandidateRooms(ce *commands.Event, login *bridgev2.UserLogin) ([]*candidateRoom, error) {
	candidatesMap := make(map[id.RoomID]*candidateRoom)

	excludeRooms := map[id.RoomID]struct{}{
		ce.User.ManagementRoom: {},
	}
	if login != nil && login.SpaceRoom != "" {
		excludeRooms[login.SpaceRoom] = struct{}{}
	}

	userPortals, err := ce.Bridge.DB.UserPortal.GetAllForLogin(ce.Ctx, login.UserLogin)
	if err == nil {
		for _, up := range userPortals {
			portal, err := ce.Bridge.GetExistingPortalByKey(ce.Ctx, up.Portal)
			if err != nil || portal == nil || portal.MXID == "" {
				continue
			}
			if _, ok := excludeRooms[portal.MXID]; ok {
				continue
			}
			if portal.RoomType == database.RoomTypeSpace {
				continue
			}
			stats := getRoomPruneStats(ce, portal)
			targetJID := stats.targetJID
			keys := computeRoomMatchKeys(ce.Ctx, ce.Bridge, targetJID, "", stats.name)
			candidatesMap[portal.MXID] = &candidateRoom{
				roomID:       portal.MXID,
				portal:       portal,
				targetJID:    targetJID,
				name:         stats.name,
				messageCount: stats.messageCount,
				lastTS:       stats.lastTS,
				preview:      stats.preview,
				isOrphan:     false,
				matchKeys:    keys,
			}
		}
	}

	mc, ok := ce.Bridge.Matrix.(*matrix.Connector)
	if ok && mc != nil && mc.AS != nil {
		botClient := mc.AS.BotClient()
		if login != nil && login.SpaceRoom != "" {
			if state, err := botClient.State(ce.Ctx, login.SpaceRoom); err == nil && state != nil {
				if childEvents, ok := state[event.StateSpaceChild]; ok {
					for stateKey, childEvent := range childEvents {
						roomID := id.RoomID(stateKey)
						if roomID == "" {
							continue
						}
						if _, excluded := excludeRooms[roomID]; excluded {
							continue
						}
						if _, exists := candidatesMap[roomID]; exists {
							continue
						}
						if childEvent == nil || childEvent.Content.AsSpaceChild() == nil {
							continue
						}

						portal, _ := ce.Bridge.GetPortalByMXID(ce.Ctx, roomID)
						candidate := &candidateRoom{
							roomID:   roomID,
							portal:   portal,
							isOrphan: portal == nil,
						}
						if portal != nil {
							stats := getRoomPruneStats(ce, portal)
							candidate.targetJID = stats.targetJID
							candidate.name = stats.name
							candidate.messageCount = stats.messageCount
							candidate.lastTS = stats.lastTS
							candidate.preview = stats.preview
						} else {
							nameEvent, _ := mc.GetStateEvent(ce.Ctx, roomID, event.StateRoomName, "")
							if nameEvent != nil && nameEvent.Content.AsRoomName() != nil {
								candidate.name = strings.TrimSpace(nameEvent.Content.AsRoomName().Name)
							}
						}
						if candidate.name == "" {
							candidate.name = candidate.targetJID
						}
						if candidate.name == "" {
							candidate.name = string(roomID)
						}
						candidate.matchKeys = computeRoomMatchKeys(ce.Ctx, ce.Bridge, candidate.targetJID, "", candidate.name)
						candidatesMap[roomID] = candidate
					}
				}
			}
		}
		joined, err := botClient.JoinedRooms(ce.Ctx)
		if err == nil && joined != nil {
			for _, roomID := range joined.JoinedRooms {
				if _, seen := excludeRooms[roomID]; seen {
					continue
				}
				if _, exists := candidatesMap[roomID]; exists {
					continue
				}

				createEvt, _ := mc.GetStateEvent(ce.Ctx, roomID, event.StateCreate, "")
				if createEvt != nil && createEvt.Content.AsCreate() != nil && createEvt.Content.AsCreate().Type == event.RoomTypeSpace {
					continue
				}

				members, err := mc.GetMembers(ce.Ctx, roomID)
				if err != nil || len(members) < 2 {
					continue
				}

				var ghosts []networkid.UserID
				var humans []id.UserID
				for mxid, member := range members {
					if mxid == ce.Bridge.Bot.GetMXID() || !member.Membership.IsInviteOrJoin() {
						continue
					}
					if ghost, ok := mc.ParseGhostMXID(mxid); ok {
						ghosts = append(ghosts, ghost)
					} else {
						humans = append(humans, mxid)
					}
				}
				if len(humans) > 1 {
					continue
				}
				if len(humans) == 1 && humans[0] != ce.User.MXID {
					continue
				}

				var ghostJID string
				if len(ghosts) == 1 {
					ghostJID = string(ghosts[0])
				}

				roomName := ""
				nameEvt, _ := mc.GetStateEvent(ce.Ctx, roomID, event.StateRoomName, "")
				if nameEvt != nil && nameEvt.Content.AsRoomName() != nil {
					roomName = strings.TrimSpace(nameEvt.Content.AsRoomName().Name)
				}

				portal, _ := ce.Bridge.GetPortalByMXID(ce.Ctx, roomID)
				var targetJID string
				var msgCount int
				var lastTS time.Time
				var preview string

				if portal != nil {
					stats := getRoomPruneStats(ce, portal)
					targetJID = stats.targetJID
					if roomName == "" {
						roomName = stats.name
					}
					msgCount = stats.messageCount
					lastTS = stats.lastTS
					preview = stats.preview
				} else if ghostJID != "" {
					targetJID = ghostJID
					if roomName == "" {
						roomName = contactDisplayName(ce.Ctx, ce.Bridge, ghostJID)
					}
				}

				if roomName == "" && targetJID != "" {
					roomName = targetJID
				}
				if roomName == "" {
					roomName = string(roomID)
				}

				keys := computeRoomMatchKeys(ce.Ctx, ce.Bridge, targetJID, ghostJID, roomName)
				candidatesMap[roomID] = &candidateRoom{
					roomID:       roomID,
					portal:       portal,
					targetJID:    targetJID,
					name:         roomName,
					messageCount: msgCount,
					lastTS:       lastTS,
					preview:      preview,
					isOrphan:     portal == nil,
					matchKeys:    keys,
				}
			}
		}
	}

	var res []*candidateRoom
	for _, c := range candidatesMap {
		res = append(res, c)
	}
	return res, nil
}

type duplicateRoomCluster struct {
	targetJID  string
	name       string
	rooms      []*candidateRoom
	primary    *candidateRoom
	duplicates []*candidateRoom
}

func sortCandidateRoomsDesc(rooms []*candidateRoom) {
	sort.Slice(rooms, func(i, j int) bool {
		if rooms[i].messageCount != rooms[j].messageCount {
			return rooms[i].messageCount > rooms[j].messageCount
		}
		if !rooms[i].lastTS.Equal(rooms[j].lastTS) {
			return rooms[i].lastTS.After(rooms[j].lastTS)
		}
		if (rooms[i].portal != nil) != (rooms[j].portal != nil) {
			return rooms[i].portal != nil
		}
		return string(rooms[i].roomID) < string(rooms[j].roomID)
	})
}

func clusterCandidateRooms(candidates []*candidateRoom) []*duplicateRoomCluster {
	n := len(candidates)
	if n == 0 {
		return nil
	}

	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		if parent[i] != i {
			parent[i] = find(parent[i])
		}
		return parent[i]
	}
	union := func(i, j int) {
		rootI := find(i)
		rootJ := find(j)
		if rootI != rootJ {
			parent[rootI] = rootJ
		}
	}

	keyToRooms := make(map[string][]int)
	for i, c := range candidates {
		for k := range c.matchKeys {
			keyToRooms[k] = append(keyToRooms[k], i)
		}
	}

	for _, roomIndices := range keyToRooms {
		if len(roomIndices) <= 1 {
			continue
		}
		for a := 0; a < len(roomIndices); a++ {
			for b := a + 1; b < len(roomIndices); b++ {
				i := roomIndices[a]
				j := roomIndices[b]
				r1 := candidates[i]
				r2 := candidates[j]
				j1 := bareJID(UnescapeJID(r1.targetJID))
				j2 := bareJID(UnescapeJID(r2.targetJID))
				if j1 != "" && j2 != "" && !strings.EqualFold(j1, j2) {
					continue
				}
				union(i, j)
			}
		}
	}

	groups := make(map[int][]*candidateRoom)
	for i, c := range candidates {
		root := find(i)
		groups[root] = append(groups[root], c)
	}

	var clusters []*duplicateRoomCluster
	for _, roomList := range groups {
		if len(roomList) == 0 {
			continue
		}
		sortCandidateRoomsDesc(roomList)

		primary := roomList[0]
		var duplicates []*candidateRoom
		if len(roomList) > 1 {
			duplicates = roomList[1:]
		}

		bestJID := primary.targetJID
		bestName := primary.name
		for _, r := range roomList {
			if bestJID == "" && r.targetJID != "" {
				bestJID = r.targetJID
			}
			if (bestName == "" || bestName == string(bestJID)) && r.name != "" && r.name != string(r.targetJID) {
				bestName = r.name
			}
		}
		if bestName == "" {
			bestName = bestJID
		}

		clusters = append(clusters, &duplicateRoomCluster{
			targetJID:  bestJID,
			name:       bestName,
			rooms:      roomList,
			primary:    primary,
			duplicates: duplicates,
		})
	}

	sort.Slice(clusters, func(i, j int) bool {
		return clusters[i].name < clusters[j].name
	})

	return clusters
}

var CommandPruneRooms = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		confirm, emptyOnly, days, err := parsePruneRoomsArgs(ce.Args)
		if err != nil {
			ce.Reply("%v", err)
			return
		}
		login := ce.User.GetDefaultLogin()
		if login == nil {
			ce.Reply("No login found")
			return
		}

		candidates, err := collectCandidateRooms(ce, login)
		if err != nil {
			ce.Reply("Failed to list rooms: %v", err)
			return
		}

		cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
		var staleCandidates []*candidateRoom

		for _, c := range candidates {
			if emptyOnly {
				if c.messageCount == 0 {
					staleCandidates = append(staleCandidates, c)
				}
			} else {
				if c.messageCount == 0 || (!c.lastTS.IsZero() && c.lastTS.Before(cutoff)) {
					staleCandidates = append(staleCandidates, c)
				}
			}
		}

		if len(staleCandidates) == 0 {
			if emptyOnly {
				ce.Reply("No creation-only empty rooms found. All %d room(s) have message activity.", len(candidates))
			} else {
				ce.Reply("No stale rooms found (threshold: %d days). All %d room(s) have recent activity.", days, len(candidates))
			}
			return
		}

		if !confirm {
			var sb strings.Builder
			if emptyOnly {
				sb.WriteString(fmt.Sprintf("Found %d creation-only room(s) with no message activity:\n", len(staleCandidates)))
			} else {
				sb.WriteString(fmt.Sprintf("Found %d stale room(s) (threshold: %d days):\n", len(staleCandidates), days))
			}
			for _, s := range staleCandidates {
				preview := s.preview
				if preview == "" {
					if s.messageCount == 0 {
						preview = "(creation only, no messages)"
					} else {
						preview = "(no preview)"
					}
				}
				statusTag := "Inactive"
				if s.messageCount == 0 {
					statusTag = "Creation only"
				}
				if s.isOrphan {
					statusTag += ", Orphaned"
				}
				sb.WriteString(fmt.Sprintf("- **%s** [%s] (Name: %q, Room: `%s`)\n", s.targetJID, statusTag, s.name, s.roomID))
				sb.WriteString(fmt.Sprintf("  - Messages: %d | Last active: %s | Last message: %q\n", s.messageCount, formatLastActivity(s.lastTS), preview))
			}
			if emptyOnly {
				sb.WriteString("\nTo permanently delete these empty creation-only rooms, run:\n`prune-rooms empty confirm`")
			} else {
				sb.WriteString(fmt.Sprintf("\nTo permanently delete these stale rooms, run:\n`prune-rooms confirm %d`\n(Or run `prune-rooms empty confirm` to only delete rooms with 0 messages)", days))
			}
			ce.Reply("%s", sb.String())
			return
		}

		var deletedCount int
		var errs []string
		for _, s := range staleCandidates {
			if login.SpaceRoom != "" {
				if err := removeRoomFromSpace(ce.Ctx, ce.Bridge.Bot, login.SpaceRoom, s.roomID); err != nil {
					errs = append(errs, fmt.Sprintf("unlink %s: %v", s.roomID, err))
				}
			}
			if s.portal != nil {
				if err := deleteSinglePortal(ce, s.portal); err != nil {
					errs = append(errs, fmt.Sprintf("%s (%s): %v", s.name, s.roomID, err))
				} else {
					deletedCount++
				}
			} else {
				if err := ce.Bridge.Bot.DeleteRoom(ce.Ctx, s.roomID, false); err != nil {
					errs = append(errs, fmt.Sprintf("%s (%s): %v", s.name, s.roomID, err))
				} else {
					deletedCount++
				}
			}
		}

		targetDesc := fmt.Sprintf("stale room(s) (older than %d days)", days)
		if emptyOnly {
			targetDesc = "creation-only room(s) with no message activity"
		}

		if len(errs) > 0 {
			ce.Reply("Pruned %d %s, with %d error(s):\n- %s",
				deletedCount, targetDesc, len(errs), strings.Join(errs, "\n- "))
		} else {
			ce.Reply("Successfully pruned %d %s.", deletedCount, targetDesc)
		}
	},
	Name: "prune-rooms",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionAdmin,
		Description: "List or delete stale bridged rooms or empty creation-only rooms (usage: prune-rooms [empty] [dry-run|confirm] [days])",
		Args:        "[empty] [dry-run|confirm] [days]",
	},
	RequiresLogin: true,
}

var CommandPruneDuplicates = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		confirm, err := parsePruneDuplicatesArgs(ce.Args)
		if err != nil {
			ce.Reply("%v", err)
			return
		}
		login := ce.User.GetDefaultLogin()
		if login == nil {
			ce.Reply("No login found")
			return
		}

		candidates, err := collectCandidateRooms(ce, login)
		if err != nil {
			ce.Reply("Failed to list rooms: %v", err)
			return
		}

		clusters := clusterCandidateRooms(candidates)
		var dupClusters []*duplicateRoomCluster
		var totalDuplicates int
		for _, c := range clusters {
			if len(c.duplicates) > 0 {
				dupClusters = append(dupClusters, c)
				totalDuplicates += len(c.duplicates)
			}
		}

		if totalDuplicates == 0 {
			ce.Reply("No duplicate rooms found. Each bridged contact has a single active room.")
			return
		}

		if !confirm {
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("Found %d duplicate room(s) across %d contact(s) (matching by phone number variants, contact name with suffix, and ghost JID):\n", totalDuplicates, len(dupClusters)))
			for _, c := range dupClusters {
				sb.WriteString(fmt.Sprintf("- Contact: **%s** (%s)\n", c.name, c.targetJID))
				sb.WriteString(fmt.Sprintf("  - **KEEP** (Primary/Active): `%s` (Name: %q, %d msgs, Last active: %s)\n",
					c.primary.roomID, c.primary.name, c.primary.messageCount, formatLastActivity(c.primary.lastTS)))
				for _, d := range c.duplicates {
					orphanTag := ""
					if d.isOrphan {
						orphanTag = " [Orphaned Room]"
					}
					sb.WriteString(fmt.Sprintf("  - **DELETE/CONSOLIDATE** (Duplicate%s): `%s` (Name: %q, %d msgs, Last active: %s)\n",
						orphanTag, d.roomID, d.name, d.messageCount, formatLastActivity(d.lastTS)))
				}
			}
			sb.WriteString("\nTo consolidate these rooms, replay message history into the active rooms, and permanently delete the duplicates, run:\n`prune-duplicates confirm`")
			ce.Reply("%s", sb.String())
			return
		}

		var consolidatedCount, deletedCount int
		var errs []string
		for _, c := range dupClusters {
			var portal *bridgev2.Portal
			if c.primary.portal != nil {
				portal = c.primary.portal
			} else {
				for _, d := range c.duplicates {
					if d.portal != nil {
						portal = d.portal
						break
					}
				}
			}
			if portal == nil && c.targetJID != "" {
				portalKey := networkid.PortalKey{ID: MakePortalID(c.targetJID)}
				portal, _ = ce.Bridge.GetPortalByKey(ce.Ctx, portalKey)
			}

			if portal != nil {
				_, err := rebindSinglePortal(ce, portal, c.primary.roomID, login, true)
				if err != nil {
					errs = append(errs, fmt.Sprintf("rebind primary %s (%s): %v", c.name, c.primary.roomID, err))
				} else {
					consolidatedCount++
				}
			}

			for _, d := range c.duplicates {
				if d.roomID == c.primary.roomID {
					continue
				}
				sendNotice(ce.Bridge.Bot, d.roomID, fmt.Sprintf("⚠️ This duplicate room has been consolidated into the active conversation: https://matrix.to/#/%s", c.primary.roomID))
				if login.SpaceRoom != "" {
					if err := removeRoomFromSpace(ce.Ctx, ce.Bridge.Bot, login.SpaceRoom, d.roomID); err != nil {
						errs = append(errs, fmt.Sprintf("unlink %s: %v", d.roomID, err))
					}
				}
				if d.portal != nil && (portal == nil || d.portal.PortalKey != portal.PortalKey) {
					if err := deleteSinglePortal(ce, d.portal); err != nil {
						errs = append(errs, fmt.Sprintf("delete portal %s (%s): %v", d.name, d.roomID, err))
					} else {
						deletedCount++
					}
				} else {
					if err := ce.Bridge.Bot.DeleteRoom(ce.Ctx, d.roomID, false); err != nil {
						errs = append(errs, fmt.Sprintf("delete %s (%s): %v", d.name, d.roomID, err))
					} else {
						deletedCount++
					}
				}
			}
		}

		if len(errs) > 0 {
			ce.Reply("Consolidated %d contact(s) and pruned %d duplicate room(s) with %d error(s):\n- %s",
				consolidatedCount, deletedCount, len(errs), strings.Join(errs, "\n- "))
		} else {
			ce.Reply("Successfully consolidated %d contact(s) into their active rooms with history replay, and deleted %d duplicate room(s).",
				consolidatedCount, deletedCount)
		}
	},
	Name: "prune-duplicates",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionAdmin,
		Description: "Find and delete duplicate rooms for contacts across phone/name variants, consolidating into the primary active room (usage: prune-duplicates [dry-run|confirm])",
		Args:        "[dry-run|confirm]",
	},
	RequiresLogin: true,
}

func findPortalForTarget(ctx context.Context, ce *commands.Event, login *bridgev2.UserLogin, target string) (*bridgev2.Portal, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, fmt.Errorf("target identifier is empty")
	}

	if strings.HasPrefix(target, "!") {
		portal, err := ce.Bridge.GetPortalByMXID(ctx, id.RoomID(target))
		if err == nil && portal != nil {
			return portal, nil
		}
	}

	targetJID := normalizeJID(target)
	if targetJID != "" {
		portalKey := networkid.PortalKey{ID: MakePortalID(targetJID)}
		portal, err := ce.Bridge.GetExistingPortalByKey(ctx, portalKey)
		if err == nil && portal != nil {
			return portal, nil
		}
	}

	rawKey := networkid.PortalKey{ID: MakePortalID(target)}
	portal, err := ce.Bridge.GetExistingPortalByKey(ctx, rawKey)
	if err == nil && portal != nil {
		return portal, nil
	}

	if login != nil {
		candidates, err := collectCandidateRooms(ce, login)
		if err == nil && len(candidates) > 0 {
			targetLower := strings.ToLower(target)
			targetClean := strings.ToLower(stripContactSuffixAny(target))
			targetDigits := extractPhoneDigits(target)
			for _, c := range candidates {
				if c.portal == nil {
					continue
				}
				cNameLower := strings.ToLower(stripContactSuffixAny(c.name))
				if cNameLower == targetLower || cNameLower == targetClean {
					return c.portal, nil
				}
				if targetDigits != "" {
					if extractPhoneDigits(c.name) == targetDigits || extractPhoneDigits(c.targetJID) == targetDigits {
						return c.portal, nil
					}
				}
			}
		}
	}

	if targetJID != "" {
		portalKey := networkid.PortalKey{ID: MakePortalID(targetJID)}
		return ce.Bridge.GetPortalByKey(ctx, portalKey)
	}

	return nil, fmt.Errorf("portal not found for %q", target)
}

var CommandDeleteRoom = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		if len(ce.Args) == 0 {
			ce.Reply("Usage: delete-room <room_id | jid | phone_number>")
			return
		}
		target := strings.TrimSpace(ce.Args[0])
		login := ce.User.GetDefaultLogin()
		if login == nil {
			ce.Reply("No login found")
			return
		}

		portal, err := findPortalForTarget(ce.Ctx, ce, login, target)
		if err != nil || portal == nil {
			ce.Reply("Portal room not found for %q. Provide a valid Matrix room ID (!...) or contact phone/JID/name.", target)
			return
		}

		portalName := portal.Name
		if portalName == "" {
			portalName = UnescapeJID(string(portal.ID))
		}
		portalMXID := portal.MXID
		targetJID := UnescapeJID(string(portal.ID))

		if err := deleteSinglePortal(ce, portal); err != nil {
			ce.Reply("Failed to delete room %s (%s): %v", portalName, portalMXID, err)
			return
		}
		ce.Reply("Successfully deleted room %q (Target: %s, MXID: %s).", portalName, targetJID, portalMXID)
	},
	Name: "delete-room",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionAdmin,
		Description: "Permanently delete a single bridged room by Matrix room ID or JID/phone",
		Args:        "<room_id | jid>",
	},
	RequiresLogin: true,
}

func parseRebindRoomArgs(args []string) (targetContact string, roomID id.RoomID, err error) {
	if len(args) != 2 {
		return "", "", fmt.Errorf("usage: rebind-room <target_jid_or_phone_or_room> <matrix_room_id>")
	}
	arg0 := strings.TrimSpace(args[0])
	arg1 := strings.TrimSpace(args[1])

	if strings.HasPrefix(arg0, "!") && strings.HasPrefix(arg1, "!") {
		return arg0, id.RoomID(arg1), nil
	} else if strings.HasPrefix(arg0, "!") {
		return arg1, id.RoomID(arg0), nil
	} else if strings.HasPrefix(arg1, "!") {
		return arg0, id.RoomID(arg1), nil
	}
	return "", "", fmt.Errorf("one of the arguments must be a Matrix room ID starting with '!' (got %q and %q)", arg0, arg1)
}

func parseReplaceRoomArgs(args []string) (target string, deleteOld bool, err error) {
	if len(args) == 0 {
		return "", false, fmt.Errorf("usage: replace-room <room_id | jid | phone_number> [delete-old]")
	}
	for _, arg := range args {
		argTrim := strings.TrimSpace(arg)
		lower := strings.ToLower(argTrim)
		if lower == "delete-old" || lower == "--delete-old" || lower == "delete" {
			deleteOld = true
		} else if target == "" {
			target = argTrim
		} else {
			return "", false, fmt.Errorf("unexpected argument %q (usage: replace-room <room_id | jid | phone_number> [delete-old])", arg)
		}
	}
	if target == "" {
		return "", false, fmt.Errorf("missing target room ID or contact JID/phone")
	}
	return target, deleteOld, nil
}

func parseReplaceAllArgs(args []string) (confirm bool, deleteOld bool, err error) {
	for _, arg := range args {
		lower := strings.ToLower(strings.TrimSpace(arg))
		if lower == "" {
			continue
		}
		switch lower {
		case "confirm", "yes", "--yes":
			confirm = true
		case "dry-run", "--dry-run":
			confirm = false
		case "delete-old", "--delete-old", "delete":
			deleteOld = true
		case "keep-old", "--keep-old":
			deleteOld = false
		default:
			return false, false, fmt.Errorf("unknown argument %q (usage: replace-all [delete-old] [dry-run|confirm])", arg)
		}
	}
	return confirm, deleteOld, nil
}

func sortRoomPruneStatsDesc(statsList []*roomPruneStats) {
	sort.Slice(statsList, func(i, j int) bool {
		if statsList[i].messageCount != statsList[j].messageCount {
			return statsList[i].messageCount > statsList[j].messageCount
		}
		if !statsList[i].lastTS.Equal(statsList[j].lastTS) {
			return statsList[i].lastTS.After(statsList[j].lastTS)
		}
		return string(statsList[i].portal.MXID) < string(statsList[j].portal.MXID)
	})
}

func rebindSinglePortal(ce *commands.Event, portal *bridgev2.Portal, targetRoomID id.RoomID, login *bridgev2.UserLogin, force bool) (oldBoundRoom id.RoomID, err error) {
	if portal == nil {
		return "", fmt.Errorf("portal is nil")
	}
	if targetRoomID == "" || !strings.HasPrefix(string(targetRoomID), "!") {
		return "", fmt.Errorf("invalid Matrix room ID: %q", targetRoomID)
	}

	existing, err := ce.Bridge.GetPortalByMXID(ce.Ctx, targetRoomID)
	if err == nil && existing != nil && existing.PortalKey != portal.PortalKey && !force {
		return "", fmt.Errorf(
			"room %s is already bridged to %s; refusing to rebind %s onto it (use `rebind-all confirm` to merge duplicates, or `delete-room %s` first)",
			targetRoomID, UnescapeJID(string(existing.ID)), formatPortalName(portal), targetRoomID,
		)
	}

	err = ce.Bridge.Bot.EnsureJoined(ce.Ctx, targetRoomID)
	if err != nil {
		return "", fmt.Errorf("bridge bot could not join room %s (please ensure the bridge bot is invited to the room): %w", targetRoomID, err)
	}

	targetJID := UnescapeJID(string(portal.ID))
	for _, participant := range portalParticipantJIDs(portal) {
		ghost, err := ce.Bridge.GetGhostByID(ce.Ctx, MakeUserID(participant))
		if err != nil {
			return "", fmt.Errorf("failed to load ghost for %s: %w", participant, err)
		}
		if ghost != nil {
			if err := ghost.Intent.EnsureJoined(ce.Ctx, targetRoomID); err != nil {
				return "", fmt.Errorf("failed to join ghost %s: %w", participant, err)
			}
		}
	}

	info, err := login.Client.GetChatInfo(ce.Ctx, portal)
	if err != nil || info == nil {
		name := dmRoomName(ce.Ctx, ce.Bridge, targetJID, "", "")
		info = &bridgev2.ChatInfo{
			Name: &name,
			Type: ptr.Ptr(database.RoomTypeDM),
		}
	}

	oldBoundRoom = portal.MXID
	if oldBoundRoom != "" && oldBoundRoom != targetRoomID && login.SpaceRoom != "" {
		_ = removeRoomFromSpace(ce.Ctx, ce.Bridge.Bot, login.SpaceRoom, oldBoundRoom)
	}
	err = portal.UpdateMatrixRoomID(ce.Ctx, targetRoomID, bridgev2.UpdateMatrixRoomIDParams{
		FailIfMXIDSet:      false,
		OverwriteOldPortal: force,
		TombstoneOldRoom:   false,
		DeleteOldRoom:      false,
		ChatInfo:           info,
		ChatInfoSource:     login,
	})
	if err != nil {
		return oldBoundRoom, fmt.Errorf("failed to update portal matrix room ID: %w", err)
	}

	login.MarkInPortal(ce.Ctx, portal)
	queuePortalBackfill(ce, portal, login, info)
	triggerCatchup(ce)

	return oldBoundRoom, nil
}

func formatPortalName(portal *bridgev2.Portal) string {
	if portal == nil {
		return "(unknown)"
	}
	if portal.Name != "" {
		return portal.Name
	}
	return UnescapeJID(string(portal.ID))
}

func queuePortalBackfill(ce *commands.Event, portal *bridgev2.Portal, login *bridgev2.UserLogin, info *bridgev2.ChatInfo) {
	if ce == nil || ce.Bridge == nil || portal == nil || login == nil || info == nil {
		return
	}
	if !info.CanBackfill || portal.RoomType == database.RoomTypeSpace {
		return
	}
	err := ce.Bridge.DB.BackfillTask.Upsert(ce.Ctx, &database.BackfillTask{
		PortalKey:         portal.PortalKey,
		UserLoginID:       login.ID,
		NextDispatchMinTS: time.Now().Add(bridgev2.BackfillMinBackoffAfterRoomCreate),
	})
	if err != nil {
		ce.Log.Err(err).Str("portal_id", string(portal.ID)).Msg("Failed to queue backfill task after room operation")
		return
	}
	ce.Bridge.WakeupBackfillQueue()
}

func triggerCatchup(ce *commands.Event) {
	login := ce.User.GetDefaultLogin()
	if login == nil {
		return
	}
	client, ok := login.Client.(*MyNetworkClient)
	if !ok || client == nil {
		return
	}
	client.triggerAutomaticDMCatchup()
	client.triggerAutomaticMUCCatchup()
}

func replaceSinglePortal(ce *commands.Event, portal *bridgev2.Portal, login *bridgev2.UserLogin, deleteOld bool) (id.RoomID, error) {
	if portal == nil {
		return "", fmt.Errorf("portal is nil")
	}

	targetJID := UnescapeJID(string(portal.ID))
	info, err := login.Client.GetChatInfo(ce.Ctx, portal)
	if err != nil || info == nil {
		name := dmRoomName(ce.Ctx, ce.Bridge, targetJID, "", "")
		info = &bridgev2.ChatInfo{
			Name: &name,
			Type: ptr.Ptr(database.RoomTypeDM),
		}
	}

	roomName := portal.Name
	if roomName == "" && info.Name != nil {
		roomName = *info.Name
	}
	if roomName == "" {
		roomName = targetJID
	}

	inviteList := []id.UserID{ce.User.MXID}
	for _, participant := range portalParticipantJIDs(portal) {
		ghost, err := ce.Bridge.GetGhostByID(ce.Ctx, MakeUserID(participant))
		if err != nil {
			return "", fmt.Errorf("failed to load ghost for %s: %w", participant, err)
		}
		if ghost != nil {
			inviteList = append(inviteList, ghost.Intent.GetMXID())
		}
	}

	req := &mautrix.ReqCreateRoom{
		Visibility: "private",
		Preset:     "private_chat",
		IsDirect:   portal.RoomType == database.RoomTypeDM && !portalUsesGroupChat(portal),
		Name:       roomName,
		Invite:     inviteList,
		PowerLevelOverride: &event.PowerLevelsEventContent{
			Events: map[string]int{
				event.StateTombstone.Type:  100,
				event.StateEncryption.Type: 100,
			},
			Users: map[id.UserID]int{
				ce.Bridge.Bot.GetMXID(): 9001,
				ce.User.MXID:            50,
			},
		},
	}

	newRoomID, err := ce.Bridge.Bot.CreateRoom(ce.Ctx, req)
	if err != nil {
		return "", fmt.Errorf("failed to create new Matrix room: %w", err)
	}

	oldMXID := portal.MXID

	for _, participant := range portalParticipantJIDs(portal) {
		ghost, err := ce.Bridge.GetGhostByID(ce.Ctx, MakeUserID(participant))
		if err != nil {
			return newRoomID, fmt.Errorf("failed to load ghost for %s after creating room: %w", participant, err)
		}
		if ghost != nil {
			if err := ghost.Intent.EnsureJoined(ce.Ctx, newRoomID); err != nil {
				return newRoomID, fmt.Errorf("failed to join ghost %s: %w", participant, err)
			}
		}
	}

	err = portal.UpdateMatrixRoomID(ce.Ctx, newRoomID, bridgev2.UpdateMatrixRoomIDParams{
		FailIfMXIDSet:      false,
		OverwriteOldPortal: true,
		TombstoneOldRoom:   !deleteOld,
		DeleteOldRoom:      deleteOld,
		ChatInfo:           info,
		ChatInfoSource:     login,
	})
	if err != nil {
		return newRoomID, fmt.Errorf("failed to bind portal to new room %s: %w", newRoomID, err)
	}

	if oldMXID != "" && oldMXID != newRoomID && !deleteOld {
		sendNotice(ce.Bridge.Bot, oldMXID, fmt.Sprintf("⚠️ This conversation has been replaced with a fresh room: https://matrix.to/#/%s", newRoomID))
	}
	if oldMXID != "" && oldMXID != newRoomID && login.SpaceRoom != "" {
		_ = removeRoomFromSpace(ce.Ctx, ce.Bridge.Bot, login.SpaceRoom, oldMXID)
	}

	login.MarkInPortal(ce.Ctx, portal)
	queuePortalBackfill(ce, portal, login, info)
	triggerCatchup(ce)

	return newRoomID, nil
}

var CommandRebindRoom = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		targetContact, targetRoomID, err := parseRebindRoomArgs(ce.Args)
		if err != nil {
			ce.Reply("%v", err)
			return
		}
		login := ce.User.GetDefaultLogin()
		if login == nil {
			ce.Reply("No login found")
			return
		}

		portal, err := findPortalForTarget(ce.Ctx, ce, login, targetContact)
		if err != nil || portal == nil {
			if strings.HasPrefix(targetContact, "!") {
				altPortal, altErr := findPortalForTarget(ce.Ctx, ce, login, string(targetRoomID))
				if altErr == nil && altPortal != nil {
					portal = altPortal
					targetRoomID = id.RoomID(targetContact)
				}
			}
		}
		if portal == nil {
			ce.Reply("Could not find portal for %q: %v", targetContact, err)
			return
		}

		oldBoundRoom, err := rebindSinglePortal(ce, portal, targetRoomID, login, false)
		if err != nil {
			ce.Reply("Failed to rebind room: %v", err)
			return
		}

		targetJID := UnescapeJID(string(portal.ID))
		var supersededNote string
		if oldBoundRoom != "" && oldBoundRoom != targetRoomID {
			supersededNote = fmt.Sprintf("\n- Superseded previous room: `%s`", oldBoundRoom)
		}

		ce.Reply("Successfully rebound contact **%s** to Matrix room `%s` (Name: %q).%s\n- Added to Space and queued history backfill.",
			targetJID, targetRoomID, portal.Name, supersededNote)
	},
	Name: "rebind-room",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionChats,
		Description: "Rebind an existing Matrix room back to a contact and add it to your Space (usage: rebind-room <target_jid_or_phone> <matrix_room_id>)",
		Args:        "<target_jid_or_phone> <matrix_room_id>",
	},
	RequiresLogin: true,
}

var CommandRebindAll = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		confirm, err := parsePruneDuplicatesArgs(ce.Args)
		if err != nil {
			ce.Reply("%v", err)
			return
		}
		login := ce.User.GetDefaultLogin()
		if login == nil {
			ce.Reply("No login found")
			return
		}

		candidates, err := collectCandidateRooms(ce, login)
		if err != nil {
			ce.Reply("Failed to list rooms: %v", err)
			return
		}

		clusters := clusterCandidateRooms(candidates)

		type rebindPlan struct {
			kind        string // "rebind" | "prune"
			reason      string
			contactName string
			targetJID   string
			portal      *bridgev2.Portal
			currentRoom id.RoomID
			targetRoom  id.RoomID
			targetName  string
			targetMsgs  int
			targetLast  time.Time
		}

		var plans []rebindPlan

		for _, cluster := range clusters {
			var portal *bridgev2.Portal
			if cluster.primary.portal != nil {
				portal = cluster.primary.portal
			} else {
				for _, r := range cluster.rooms {
					if r.portal != nil {
						portal = r.portal
						break
					}
				}
			}
			if portal == nil && cluster.targetJID != "" {
				portalKey := networkid.PortalKey{ID: MakePortalID(cluster.targetJID)}
				portal, _ = ce.Bridge.GetPortalByKey(ce.Ctx, portalKey)
			}

			if portal != nil && portal.MXID != cluster.primary.roomID {
				plans = append(plans, rebindPlan{
					kind:        "rebind",
					reason:      "rebind contact to historical primary room with most message activity",
					contactName: cluster.name,
					targetJID:   cluster.targetJID,
					portal:      portal,
					currentRoom: portal.MXID,
					targetRoom:  cluster.primary.roomID,
					targetName:  cluster.primary.name,
					targetMsgs:  cluster.primary.messageCount,
					targetLast:  cluster.primary.lastTS,
				})
			}

			for _, dup := range cluster.duplicates {
				if dup.roomID == cluster.primary.roomID {
					continue
				}
				plans = append(plans, rebindPlan{
					kind:        "prune",
					reason:      fmt.Sprintf("duplicate room of %s (%s)", cluster.name, cluster.primary.roomID),
					contactName: cluster.name,
					targetJID:   cluster.targetJID,
					portal:      dup.portal,
					currentRoom: dup.roomID,
					targetRoom:  dup.roomID,
					targetName:  dup.name,
					targetMsgs:  dup.messageCount,
					targetLast:  dup.lastTS,
				})
			}
		}

		if len(plans) == 0 {
			ce.Reply("No duplicate or orphaned rooms found to rebind. All contacts have single active rooms.")
			return
		}

		if !confirm {
			var sb strings.Builder
			var rebinds, prunes int
			for _, p := range plans {
				switch p.kind {
				case "rebind":
					rebinds++
					sb.WriteString(fmt.Sprintf("- **Rebind**: **%s** (%s) current: `%s` → `%s` (Name: %q, %d msgs, last active %s; reason: %s)\n",
						p.contactName, p.targetJID, p.currentRoom, p.targetRoom, p.targetName, p.targetMsgs, formatLastActivity(p.targetLast), p.reason))
				case "prune":
					prunes++
					sb.WriteString(fmt.Sprintf("- **Prune/Consolidate**: **%s** `%s` (Name: %q, %d msgs, last active %s; reason: %s)\n",
						p.contactName, p.targetRoom, p.targetName, p.targetMsgs, formatLastActivity(p.targetLast), p.reason))
				}
			}
			sb.WriteString(fmt.Sprintf("\nFound %d rebind(s), %d prune/consolidate(s).\nTo execute, rebind active rooms with history replay, and prune duplicates, run:\n`rebind-all confirm`",
				rebinds, prunes))
			ce.Reply("%s", sb.String())
			return
		}

		var reboundCount, prunedCount int
		var errs []string

		for _, p := range plans {
			switch p.kind {
			case "rebind":
				if p.portal != nil {
					oldRoom, err := rebindSinglePortal(ce, p.portal, p.targetRoom, login, true)
					if err != nil {
						errs = append(errs, fmt.Sprintf("%s (%s): %v", p.targetJID, p.targetRoom, err))
					} else {
						reboundCount++
						if oldRoom != "" && oldRoom != p.targetRoom {
							sendNotice(ce.Bridge.Bot, oldRoom, fmt.Sprintf("⚠️ This room has been rebound to https://matrix.to/#/%s", p.targetRoom))
						}
					}
				}
			case "prune":
				if login.SpaceRoom != "" {
					_ = removeRoomFromSpace(ce.Ctx, ce.Bridge.Bot, login.SpaceRoom, p.targetRoom)
				}
				if p.portal != nil {
					if err := deleteSinglePortal(ce, p.portal); err != nil {
						errs = append(errs, fmt.Sprintf("prune %s (%s): %v", p.targetJID, p.targetRoom, err))
					} else {
						prunedCount++
					}
				} else {
					if err := ce.Bridge.Bot.DeleteRoom(ce.Ctx, p.targetRoom, false); err != nil {
						errs = append(errs, fmt.Sprintf("delete %s (%s): %v", p.targetJID, p.targetRoom, err))
					} else {
						prunedCount++
					}
				}
			}
		}

		if len(errs) > 0 {
			ce.Reply("Rebound %d contact(s) (pruned/consolidated %d duplicate rooms), with %d error(s):\n- %s",
				reboundCount, prunedCount, len(errs), strings.Join(errs, "\n- "))
		} else {
			ce.Reply("Successfully rebound %d contact(s) back to their historical active rooms with history replay and added them to your Space (pruned/consolidated %d duplicate rooms).",
				reboundCount, prunedCount)
		}
	},
	Name: "rebind-all",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionChats,
		Description: "Automatically rebind all contacts to their historical rooms with message history and clean up duplicate rooms across phone/name variants (usage: rebind-all [dry-run|confirm])",
		Args:        "[dry-run|confirm]",
	},
	RequiresLogin: true,
}

var CommandReplaceRoom = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		target, deleteOld, err := parseReplaceRoomArgs(ce.Args)
		if err != nil {
			ce.Reply("%v", err)
			return
		}
		login := ce.User.GetDefaultLogin()
		if login == nil {
			ce.Reply("No login found")
			return
		}

		portal, err := findPortalForTarget(ce.Ctx, ce, login, target)
		if err != nil || portal == nil {
			ce.Reply("Portal room not found for %q. Provide a valid Matrix room ID (!...) or contact phone/JID/name.", target)
			return
		}

		targetJID := UnescapeJID(string(portal.ID))
		newRoomID, err := replaceSinglePortal(ce, portal, login, deleteOld)
		if err != nil {
			ce.Reply("Failed to replace room for contact %s: %v", targetJID, err)
			return
		}

		ce.Reply("Successfully replaced room for contact **%s**.\n- New room: `%s` (Name: %q)\n- Added to Space and queued message history backfill.",
			targetJID, newRoomID, portal.Name)
	},
	Name: "replace-room",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionChats,
		Description: "Replace an existing bridged room with a fresh Matrix room and replay message history (usage: replace-room <room_id | jid | phone> [delete-old])",
		Args:        "<room_id | jid> [delete-old]",
	},
	RequiresLogin: true,
}

var CommandReplaceAll = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		confirm, deleteOld, err := parseReplaceAllArgs(ce.Args)
		if err != nil {
			ce.Reply("%v", err)
			return
		}
		login := ce.User.GetDefaultLogin()
		if login == nil {
			ce.Reply("No login found")
			return
		}

		userPortals, err := ce.Bridge.DB.UserPortal.GetAllForLogin(ce.Ctx, login.UserLogin)
		if err != nil {
			ce.Reply("Failed to list portals: %v", err)
			return
		}

		seen := make(map[networkid.PortalKey]bool)
		var portalList []*bridgev2.Portal
		for _, up := range userPortals {
			if seen[up.Portal] {
				continue
			}
			seen[up.Portal] = true
			portal, err := ce.Bridge.GetExistingPortalByKey(ce.Ctx, up.Portal)
			if err != nil || portal == nil {
				continue
			}
			portalList = append(portalList, portal)
		}

		candidates, _ := collectCandidateRooms(ce, login)
		for _, c := range candidates {
			if c.portal != nil && !seen[c.portal.PortalKey] {
				seen[c.portal.PortalKey] = true
				portalList = append(portalList, c.portal)
			}
		}

		if len(portalList) == 0 {
			ce.Reply("No active portal rooms found to replace.")
			return
		}

		if !confirm {
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("Ready to replace %d active portal room(s) with fresh Matrix rooms:\n\n", len(portalList)))
			for _, portal := range portalList {
				stats := getRoomPruneStats(ce, portal)
				sb.WriteString(fmt.Sprintf("- Contact: **%s** | Current Room: `%s` | Msgs: %d (Name: %q)\n",
					stats.targetJID, portal.MXID, stats.messageCount, stats.name))
			}
			action := "leave old rooms with redirect notices"
			cmdExample := "replace-all confirm"
			if deleteOld {
				action = "delete old Matrix rooms"
				cmdExample = "replace-all delete-old confirm"
			}
			sb.WriteString(fmt.Sprintf("\nAction on old rooms: **%s**\nTo execute, run:\n`%s`", action, cmdExample))
			ce.Reply("%s", sb.String())
			return
		}

		var replacedCount int
		var errs []string
		for _, portal := range portalList {
			targetJID := UnescapeJID(string(portal.ID))
			_, err := replaceSinglePortal(ce, portal, login, deleteOld)
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", targetJID, err))
			} else {
				replacedCount++
			}
		}

		if len(errs) > 0 {
			ce.Reply("Replaced %d room(s) with %d error(s):\n- %s",
				replacedCount, len(errs), strings.Join(errs, "\n- "))
		} else {
			ce.Reply("Successfully replaced %d portal room(s) with fresh Matrix rooms, added them to your Space, and queued message history backfill.",
				replacedCount)
		}
	},
	Name: "replace-all",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionChats,
		Description: "Bulk replace all active portal rooms with fresh Matrix rooms and replay message history (usage: replace-all [delete-old] [dry-run|confirm])",
		Args:        "[delete-old] [dry-run|confirm]",
	},
	RequiresLogin: true,
}

func parseRebindSpaceArgs(args []string) (id.RoomID, error) {
	if len(args) != 1 {
		return "", fmt.Errorf("usage: rebind-space <space_room_id>")
	}
	arg := strings.TrimSpace(args[0])
	if !strings.HasPrefix(arg, "!") {
		return "", fmt.Errorf("invalid space room ID %q (must start with '!')", arg)
	}
	return id.RoomID(arg), nil
}

func parseDeleteSpaceArgs(args []string) (spaceRoomID id.RoomID, force bool, err error) {
	if len(args) == 0 {
		return "", false, fmt.Errorf("usage: delete-space <space_room_id> [--force]")
	}
	for _, arg := range args {
		argTrim := strings.TrimSpace(arg)
		lower := strings.ToLower(argTrim)
		if lower == "--force" || lower == "force" {
			force = true
		} else if strings.HasPrefix(argTrim, "!") {
			spaceRoomID = id.RoomID(argTrim)
		} else {
			return "", false, fmt.Errorf("unexpected argument %q (usage: delete-space <space_room_id> [--force])", arg)
		}
	}
	if spaceRoomID == "" {
		return "", false, fmt.Errorf("missing space room ID (must start with '!')")
	}
	return spaceRoomID, force, nil
}

var CommandRebindSpace = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		targetSpaceRoom, err := parseRebindSpaceArgs(ce.Args)
		if err != nil {
			ce.Reply("%v", err)
			return
		}
		login := ce.User.GetDefaultLogin()
		if login == nil {
			ce.Reply("No login found")
			return
		}

		err = ce.Bridge.Bot.EnsureJoined(ce.Ctx, targetSpaceRoom)
		if err != nil {
			ce.Reply("Bridge bot could not join space room `%s`: %v", targetSpaceRoom, err)
			return
		}

		oldSpaceRoom := login.SpaceRoom
		login.SpaceRoom = targetSpaceRoom
		if err := login.Save(ce.Ctx); err != nil {
			ce.Reply("Failed to save space room to database: %v", err)
			return
		}

		if ce.User.ManagementRoom != "" {
			_ = addRoomToSpace(ce.Ctx, ce.Bridge, targetSpaceRoom, ce.User.ManagementRoom, false)
		}

		userPortals, err := ce.Bridge.DB.UserPortal.GetAllForLogin(ce.Ctx, login.UserLogin)
		var addedPortals int
		if err == nil {
			for _, up := range userPortals {
				portal, err := ce.Bridge.GetExistingPortalByKey(ce.Ctx, up.Portal)
				if err != nil || portal == nil || portal.MXID == "" {
					continue
				}
				if err := login.AddPortalToSpace(ce.Ctx, portal, up.CopyWithoutValues()); err == nil {
					addedPortals++
				}
			}
		}

		if oldSpaceRoom != "" && oldSpaceRoom != targetSpaceRoom {
			sendNotice(ce.Bridge.Bot, oldSpaceRoom, fmt.Sprintf("⚠️ This Space has been replaced by https://matrix.to/#/%s. All bridged chats have been linked to the new Space.", targetSpaceRoom))
		}

		ce.Reply("Successfully rebound personal Space to `%s`.\n- Linked management room and %d active chat portal(s) to the Space.", targetSpaceRoom, addedPortals)
	},
	Name: "rebind-space",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionChats,
		Description: "Rebind your personal Space to an existing Space room and link all active chats to it (usage: rebind-space <space_room_id>)",
		Args:        "<space_room_id>",
	},
	RequiresLogin: true,
}

var CommandResyncSpace = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		login := ce.User.GetDefaultLogin()
		if login == nil {
			ce.Reply("No login found")
			return
		}
		if login.SpaceRoom == "" {
			ce.Reply("No personal space found for this login. Run `rebind-space <space_room_id>` or `sync` first.")
			return
		}

		spaceRoom := login.SpaceRoom
		if err := ce.Bridge.Bot.EnsureJoined(ce.Ctx, spaceRoom); err != nil {
			ce.Reply("Bridge bot could not join personal space `%s`: %v", spaceRoom, err)
			return
		}

		activeChildren := make(map[id.RoomID]bool)
		if ce.User.ManagementRoom != "" {
			activeChildren[ce.User.ManagementRoom] = true
			_ = addRoomToSpace(ce.Ctx, ce.Bridge, spaceRoom, ce.User.ManagementRoom, false)
		}

		userPortals, err := ce.Bridge.DB.UserPortal.GetAllForLogin(ce.Ctx, login.UserLogin)
		var linkedChats int
		if err == nil {
			for _, up := range userPortals {
				portal, err := ce.Bridge.GetExistingPortalByKey(ce.Ctx, up.Portal)
				if err != nil || portal == nil || portal.MXID == "" {
					continue
				}
				activeChildren[portal.MXID] = true
				if err := login.AddPortalToSpace(ce.Ctx, portal, up.CopyWithoutValues()); err == nil {
					linkedChats++
				}
			}
		}

		var unlinkedStale int
		mc, ok := ce.Bridge.Matrix.(*matrix.Connector)
		if ok && mc != nil && mc.AS != nil {
			state, err := mc.AS.BotClient().State(ce.Ctx, spaceRoom)
			if err == nil && state != nil {
				if childEvents, ok := state[event.StateSpaceChild]; ok {
					for stateKey, evt := range childEvents {
						childID := id.RoomID(stateKey)
						if childID == "" {
							continue
						}
						childContent := evt.Content.AsSpaceChild()
						if childContent == nil {
							continue
						}
						if !activeChildren[childID] {
							if err := removeRoomFromSpace(ce.Ctx, ce.Bridge.Bot, spaceRoom, childID); err != nil {
								ce.Log.Warn().Err(err).Stringer("room", childID).Msg("Failed to unlink stale Space child")
								continue
							}
							unlinkedStale++
						}
					}
				}
			}
		}

		ce.Reply("Successfully resynced personal Space `%s`.\n- Linked %d active chat(s) and management room\n- Unlinked %d stale/dead room(s) from Space.",
			spaceRoom, linkedChats, unlinkedStale)
	},
	Name: "resync-space",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionChats,
		Description: "Clean up unlinked/stale rooms from your personal Space and ensure all active chat portals are linked",
	},
	RequiresLogin: true,
}

var CommandPruneSpaces = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		confirm, err := parsePruneDuplicatesArgs(ce.Args)
		if err != nil {
			ce.Reply("%v", err)
			return
		}
		login := ce.User.GetDefaultLogin()
		if login == nil {
			ce.Reply("No login found")
			return
		}

		activeSpace := login.SpaceRoom
		mc, ok := ce.Bridge.Matrix.(*matrix.Connector)
		if !ok || mc == nil || mc.AS == nil {
			ce.Reply("Matrix connector unavailable")
			return
		}

		joined, err := mc.AS.BotClient().JoinedRooms(ce.Ctx)
		if err != nil || joined == nil {
			ce.Reply("Failed to list joined rooms: %v", err)
			return
		}

		type spaceInfo struct {
			roomID      id.RoomID
			name        string
			memberCount int
		}
		var orphanedSpaces []spaceInfo

		for _, roomID := range joined.JoinedRooms {
			if roomID == activeSpace || roomID == ce.User.ManagementRoom {
				continue
			}
			createEvt, err := mc.GetStateEvent(ce.Ctx, roomID, event.StateCreate, "")
			if err != nil || createEvt == nil {
				continue
			}
			createContent := createEvt.Content.AsCreate()
			if createContent == nil || createContent.Type != event.RoomTypeSpace {
				continue
			}

			members, err := mc.GetMembers(ce.Ctx, roomID)
			if err != nil {
				continue
			}
			var humanCount int
			var isUserInRoom bool
			for mxid, m := range members {
				if !m.Membership.IsInviteOrJoin() || mxid == ce.Bridge.Bot.GetMXID() {
					continue
				}
				if _, isGhost := mc.ParseGhostMXID(mxid); !isGhost {
					humanCount++
					if mxid == ce.User.MXID {
						isUserInRoom = true
					}
				}
			}
			if humanCount > 1 || (humanCount == 1 && !isUserInRoom) {
				continue
			}

			var name string
			if nameEvt, err := mc.GetStateEvent(ce.Ctx, roomID, event.StateRoomName, ""); err == nil && nameEvt != nil {
				name = nameEvt.Content.AsRoomName().Name
			}
			if name == "" {
				name = "(unnamed Space)"
			}

			orphanedSpaces = append(orphanedSpaces, spaceInfo{
				roomID:      roomID,
				name:        name,
				memberCount: len(members),
			})
		}

		if len(orphanedSpaces) == 0 {
			ce.Reply("No orphaned Space rooms found. Active Space is `%s`.", activeSpace)
			return
		}

		if !confirm {
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("Found %d orphaned Space room(s) (Active Space: `%s`):\n\n", len(orphanedSpaces), activeSpace))
			for _, s := range orphanedSpaces {
				sb.WriteString(fmt.Sprintf("- Space: `%s` | Name: %q | Members: %d\n", s.roomID, s.name, s.memberCount))
			}
			sb.WriteString("\nTo delete these orphaned Spaces and leave them, run:\n`prune-spaces confirm`")
			ce.Reply("%s", sb.String())
			return
		}

		var deletedCount int
		var errs []string
		for _, s := range orphanedSpaces {
			if err := ce.Bridge.Bot.DeleteRoom(ce.Ctx, s.roomID, false); err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", s.roomID, err))
			} else {
				deletedCount++
			}
		}

		if len(errs) > 0 {
			ce.Reply("Pruned %d orphaned Space(s) with %d error(s):\n- %s", deletedCount, len(errs), strings.Join(errs, "\n- "))
		} else {
			ce.Reply("Successfully deleted and left %d orphaned Space room(s).", deletedCount)
		}
	},
	Name: "prune-spaces",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionAdmin,
		Description: "Find and delete old/orphaned Space rooms left behind by bridge resets (usage: prune-spaces [dry-run|confirm])",
		Args:        "[dry-run|confirm]",
	},
	RequiresLogin: true,
}

var CommandDeleteSpace = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		spaceRoomID, force, err := parseDeleteSpaceArgs(ce.Args)
		if err != nil {
			ce.Reply("%v", err)
			return
		}
		login := ce.User.GetDefaultLogin()
		if login == nil {
			ce.Reply("No login found")
			return
		}

		if spaceRoomID == login.SpaceRoom && !force {
			ce.Reply("`%s` is your currently active personal Space. Refusing to delete without `--force`.\nTo delete your active Space anyway, run `delete-space %s --force` or rebind to a new Space with `rebind-space <id>` first.", spaceRoomID, spaceRoomID)
			return
		}

		if spaceRoomID == login.SpaceRoom && force {
			login.SpaceRoom = ""
			_ = login.Save(ce.Ctx)
		}

		err = ce.Bridge.Bot.DeleteRoom(ce.Ctx, spaceRoomID, false)
		if err != nil {
			ce.Reply("Failed to delete Space room `%s`: %v", spaceRoomID, err)
			return
		}

		ce.Reply("Successfully deleted Space room `%s`.", spaceRoomID)
	},
	Name: "delete-space",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionAdmin,
		Description: "Delete a specific Space room (usage: delete-space <space_room_id> [--force])",
		Args:        "<space_room_id> [--force]",
	},
	RequiresLogin: true,
}

func deleteAllPortals(ce *commands.Event, login *bridgev2.UserLogin) error {
	userPortals, err := ce.Bridge.DB.UserPortal.GetAllForLogin(ce.Ctx, login.UserLogin)
	if err != nil {
		return fmt.Errorf("failed to list portals: %w", err)
	}
	portals := make([]*bridgev2.Portal, 0, len(userPortals))
	for _, up := range userPortals {
		portal, err := ce.Bridge.GetExistingPortalByKey(ce.Ctx, up.Portal)
		if err != nil || portal == nil {
			continue
		}
		portals = append(portals, portal)
	}

	bridgev2.DeleteManyPortals(ce.Ctx, portals, nil)
	for _, up := range userPortals {
		_ = ce.Bridge.DB.UserPortal.Delete(ce.Ctx, up)
	}

	if spaceRoom, err := login.GetSpaceRoom(ce.Ctx); err == nil && spaceRoom != "" {
		_ = ce.Bridge.Bot.DeleteRoom(ce.Ctx, spaceRoom, false)
	}

	login.Delete(ce.Ctx, status.BridgeState{StateEvent: status.StateLoggedOut}, bridgev2.DeleteOpts{
		LogoutRemote:     true,
		DontCleanupRooms: true,
		BlockingCleanup:  true,
	})
	return nil
}
