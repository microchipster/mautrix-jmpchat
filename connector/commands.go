package connector

import (
	"maunium.net/go/mautrix/bridgev2/commands"
)

var CommandSyncGroups = &commands.FullHandler{
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
		if err := client.requestBookmarks(); err != nil {
			ce.Reply("Failed to request bookmarks: %v", err)
			return
		}
		ce.Reply("Requested group chat bookmarks. Portals and joins will be processed as the server responds.")
	},
	Name: "sync-groups",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionChats,
		Description: "Sync group chats from XMPP bookmarks and join them",
	},
	RequiresLogin: true,
}
