package connector

import (
	"sort"
	"strings"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/bridgev2/database"
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
		client.fullSyncRequested = true
		client.fullSyncRosterPending = true
		client.fullSyncBookmarksPending = true
		if err := client.requestRoster(); err != nil {
			ce.Reply("Failed to request roster: %v", err)
			return
		}
		if err := client.requestBookmarks(); err != nil {
			ce.Reply("Failed to request bookmarks: %v", err)
			return
		}
		ce.Reply("Requested full sync (roster + bookmarks). MAM backfill will start as results arrive.")
	},
	Name:    "sync",
	Aliases: []string{"sync-groups"},
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionChats,
		Description: "Full sync of roster + bookmarks and MAM backfill",
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
