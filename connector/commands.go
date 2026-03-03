package connector

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
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

var CommandCreate = &commands.FullHandler{
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
			if err := client.createDM(ce.Ctx, participants[0]); err != nil {
				ce.Reply("Failed to create chat: %v", err)
				return
			}
			ce.Reply("Created chat for %s", participants[0])
			return
		}
		if err := client.createGroupChat(ce.Ctx, name, participants); err != nil {
			ce.Reply("Failed to create group chat: %v", err)
			return
		}
		ce.Reply("Created group chat with %d participants", len(participants))
	},
	Name: "create",
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

var CommandSetName = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		if len(ce.Args) < 2 {
			ce.Reply("Usage: set-name <phone or JID> <display name>")
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
		ce.Reply("Set display name for %s", jid)
	},
	Name: "set-name",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionChats,
		Description: "Set a custom display name for a user",
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
		ce.Reply("Deleted all bridged rooms and the space. You can log in again with !login.")
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
			ce.Reply("Deleted all bridged rooms and the space. You can log in again with !login.")
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

	mgmtRoom := ce.User.ManagementRoom
	if mgmtRoom != "" {
		_ = ce.Bridge.Bot.DeleteRoom(ce.Ctx, mgmtRoom, false)
		ce.User.ManagementRoom = ""
		if err := ce.User.Save(ce.Ctx); err != nil {
			ce.Bridge.Log.Warn().Err(err).Msg("Failed to clear management room after deletion")
		}
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
