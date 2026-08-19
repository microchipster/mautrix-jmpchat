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
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/bridgev2/database"
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
		Description: "Sync roster + bookmarks and MAM backfill. Usage: sync [full|<duration>] (default: 30d)",
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
		name := roomJID
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
			ChatInfo: &bridgev2.ChatInfo{
				Name: &name,
				Type: ptr.Ptr(database.RoomTypeDefault),
			},
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
	Name: "list-rooms",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionChats,
		Description: "List rooms discovered via bookmarks, disco, or joins",
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
			ce.Reply("Usage: set-contact-name <phone or JID> <display name>")
			return
		}
		jid := normalizeJID(ce.Args[0])
		if jid == "" {
			ce.Reply("Invalid JID")
			return
		}
		name := strings.TrimSpace(strings.Join(ce.Args[1:], " "))
		if name == "" {
			ce.Reply("Display name cannot be empty")
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
		meta.CustomName = name
		ghost.Metadata = meta
		ghost.UpdateInfo(ce.Ctx, &bridgev2.UserInfo{Name: &name})
		if err := ce.Bridge.DB.Ghost.Update(ce.Ctx, ghost.Ghost); err != nil {
			ce.Reply("Failed to save display name for %s: %v", jid, err)
			return
		}
		login := ce.User.GetDefaultLogin()
		if login != nil {
			suffix := defaultContactNameSuffix
			if client, ok := login.Client.(*MyNetworkClient); ok && client != nil {
				suffix = client.contactNameSuffix()
			}
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
		Description: "Set a custom display name for a user",
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
	val := strings.TrimSpace(input)
	if val == "" {
		return ""
	}
	if strings.Contains(val, "@") {
		return val
	}
	if !strings.HasPrefix(val, "+") {
		val = "+" + val
	}
	return val + "@cheogram.com"
}

func splitCheogramGroupContacts(jid string) []string {
	jid = bareJID(strings.TrimSpace(strings.ToLower(jid)))
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
		if !strings.HasPrefix(part, "+") {
			part = "+" + part
		}
		contacts = append(contacts, part+"@cheogram.com")
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
