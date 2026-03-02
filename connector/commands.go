package connector

import (
	"context"
	"sort"
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/bridgev2/database"
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
		notify := func(msg string) {
			sendNotice(ce.Bot, ce.OrigRoomID, msg)
		}
		if err := client.StartFullSync(ce.Ctx, notify); err != nil {
			ce.Reply("%v", err)
			return
		}
		ce.Reply("Sync queued. I will post progress here.")
	},
	Name:    "sync",
	Aliases: []string{"sync-groups"},
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionChats,
		Description: "Full sync of roster + bookmarks and MAM backfill",
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

var CommandNuke = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		if len(ce.Args) == 0 || (ce.Args[0] != "yes" && ce.Args[0] != "--yes") {
			ce.Reply("This will remove all bridged rooms, the space, and log you out. Run: uninstall yes")
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

		if mgmtRoom, err := ce.User.GetManagementRoom(ce.Ctx); err == nil && mgmtRoom != "" {
			_ = ce.Bridge.Bot.DeleteRoom(ce.Ctx, mgmtRoom, false)
		}
		if spaceRoom, err := login.GetSpaceRoom(ce.Ctx); err == nil && spaceRoom != "" {
			_ = ce.Bridge.Bot.DeleteRoom(ce.Ctx, spaceRoom, false)
		}

		login.Delete(ce.Ctx, status.BridgeState{StateEvent: status.StateLoggedOut}, bridgev2.DeleteOpts{
			LogoutRemote:     true,
			DontCleanupRooms: true,
			BlockingCleanup:  true,
		})
		ce.Reply("Bridge uninstall complete. You can log in again with !login.")
	},
	Name:    "uninstall",
	Aliases: []string{"nuke", "wipe"},
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionAdmin,
		Description: "Remove all bridged rooms, delete the space, and log out (use: uninstall yes)",
	},
	RequiresLogin: true,
}

var CommandLogoutSoft = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		login := ce.User.GetDefaultLogin()
		if login == nil {
			ce.Reply("No login found")
			return
		}
		login.Delete(ce.Ctx, status.BridgeState{StateEvent: status.StateLoggedOut}, bridgev2.DeleteOpts{
			LogoutRemote:     true,
			DontCleanupRooms: true,
			BlockingCleanup:  true,
		})
		ce.Reply("Logged out without removing rooms. You can log in again with !login.")
	},
	Name:    "logout-soft",
	Aliases: []string{"logout-keep", "logout-only"},
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionAdmin,
		Description: "Log out without removing rooms or the space",
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
