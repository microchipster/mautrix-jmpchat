// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"sort"
	"strings"

	"maunium.net/go/mautrix/bridgev2/commands"
)

// topicGuides holds the built-in management-room guides (`help <topic>`, or just mentioning a
// keyword like "calls"). Keep them free of deployment-specific values: no phone numbers, hostnames,
// room IDs or credentials.
var topicGuides = map[string]string{
	"calling": "**Calling**\n\n" +
		"Phone calls can ring in Matrix instead of the JMP app. Calls use the number's own chat room — encrypted rooms included — and in encrypted rooms the call audio stays end-to-end encrypted.\n\n" +
		"**Opt in, per number:**\n" +
		"1. Open your JMP account chat: send `jmp` here (or `create cheogram.com`) and send the bot `reset sip account`. It replies with the SIP username, password, server and transport. Every run issues a fresh password, so do this when you are ready to set it up.\n" +
		"2. Back here, send `sip-login` and answer the prompts: registrar, username, password. Answer each prompt as its own message; the password may contain spaces.\n" +
		"3. `sip-status` shows whether the account registered.\n\n" +
		"**How it behaves:**\n" +
		"- Incoming calls ring in the number's room for about 25 seconds. If nobody answers, the bridge declines the call and the phone network can send the caller to voicemail. The window is deliberately short: the phone network stops waiting for an answer after about half a minute, so a longer ring would only produce calls that cannot connect.\n" +
		"- Missed, busy, declined and failed calls post a notice in this management room, so a call never ends silently.\n" +
		"- While the SIP account is registered, the JMP app no longer receives calls for that number; `sip-logout` gives them back to the app.\n" +
		"- Outgoing calls start with `call` in the number's room (it asks which number to dial). During a call, `hangup` ends it and `dtmf` sends digits to menu systems.",

	"account": "**Your JMP account chat**\n\n" +
		"Send `jmp` here (aliases: `account`, `jmp-account`) — or `create cheogram.com` — to open the chat with the `cheogram.com` account bot, the same contact the JMP/Cheogram app uses for account management.\n\n" +
		"Send the bot `help` in that room for everything it can do, including:\n" +
		"- account info: phone number, balance, renewal date\n" +
		"- usage: messages, voice minutes and payments for the last 30 days\n" +
		"- transactions, top-up and billing\n" +
		"- `configure calls`: ring time before voicemail, call forwarding (to Jabber, SIP or a phone number), voicemail, caller ID name\n" +
		"- `ogm`: record your voicemail greeting (the bot calls you back)\n" +
		"- plan settings, overage limits, referral codes, number porting\n" +
		"- `reset sip account`: SIP credentials for Matrix calling (see `help calling`)\n\n" +
		"Menus answer with a number; `next` keeps the current value and `cancel` leaves the menu.",

	"sms": "**Texting**\n\n" +
		"SMS and MMS to and from your JMP numbers are bridged into Matrix: each conversation gets its own room. Group texts become Matrix group chats and picture messages arrive as attachments. Anything you send in those rooms goes out as a text from the number.\n\n" +
		"Numbers you have texted before already have a room; send `create <number>` for a new one.",

	"chats": "**Chats and contacts**\n\n" +
		"- `create <number or JID>` (alias `start-chat`) opens a new chat; with several arguments it makes a group chat.\n" +
		"- `list-contacts`, `search-roster`, `sync-roster` — see and refresh your contact list.\n" +
		"- `join-room`, `muc-nick` — join Jabber (XMPP) group chats and set the nickname used there.\n" +
		"- `block`, `report` — handle unwanted senders.\n" +
		"- `bookmarks-sync`, `remove-bookmark` — keep your Jabber bookmarks in sync.\n" +
		"- `carddav-login <email> <password>` and `carddav-sync` — the address-book (CardDAV) integration (common servers are auto-detected).\n" +
		"- `sync`, `sync-status` — refresh the network state and check the connection.",

	"encryption": "**Encryption**\n\n" +
		"Encrypted rooms are fully supported, calls included. The bridge's own messages — including the notices in this management room — are end-to-end encrypted like any other message, and call audio in an encrypted room uses the same encryption as the room.\n\n" +
		"The bridge joins calls as a dedicated call device; you do not need to verify it for messages or calls to work.",

	"troubleshooting": "**Troubleshooting**\n\n" +
		"- A call that did not connect posts a notice here saying why (missed, busy, declined, or \"could not connect audio\" when the phone network stopped confirming the call before it was answered).\n" +
		"- `sip-status` — is the SIP account set up and registered? `sip-login` sets it up again.\n" +
		"- `sync-status`, `sync` — is the bridge connected to the network?\n" +
		"- `list-logins` — which accounts this bridge serves.\n" +
		"- `prune-rooms`, `prune-duplicates` — clean up stale rooms.\n" +
		"- `delete-all-portals` — remove every bridged room (destructive).\n" +
		"- Say what is wrong in this room: mentioning a keyword like \"calls\" or \"sms\" gets you the matching guide, and typos get suggestions.",

	"docs": "**Guides available in this room:**\n\n" +
		"- `help calling` — set up and use phone calls\n" +
		"- `help account` — your JMP account chat: billing, usage, call settings, SIP credentials\n" +
		"- `help sms` — texting to and from your numbers\n" +
		"- `help chats` — chats, contacts and the roster\n" +
		"- `help encryption` — end-to-end encryption\n" +
		"- `help troubleshooting` — when something is not working\n\n" +
		"Plain `help` also lists every command; you can also just mention a keyword like `calls` or `sms`.",
}

// topicGuide returns the guide for a topic name (case-insensitive), and whether one exists.
func topicGuide(name string) (string, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "index" {
		name = "docs"
	}
	guide, ok := topicGuides[name]
	return guide, ok
}

// topicNames returns the topic names in a stable order.
func topicNames() []string {
	names := make([]string, 0, len(topicGuides))
	for name := range topicGuides {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// quotedTopics formats the topic names for replies.
func quotedTopics() string {
	quoted := make([]string, 0, len(topicGuides))
	for _, name := range topicNames() {
		if name == "docs" {
			continue
		}
		quoted = append(quoted, "`"+name+"`")
	}
	return strings.Join(quoted, ", ")
}

// docsIntro is the first line of the `help` reply.
const docsIntro = "**Docs live in this room.** `help <topic>` opens one of the guides; you can also just mention a keyword like `calls` or `sms`.\n\n**Commands:**"

// CommandHelpGuide replaces the stock help command: it lists the guides next to the commands, and
// `help <topic>` opens one of the guides above.
var CommandHelpGuide = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		if len(ce.Args) == 0 {
			ce.Reply("%s\n\n%s", docsIntro, commands.FormatHelp(ce))
			return
		}
		topic := strings.ToLower(ce.Args[0])
		if topic == "commands" {
			ce.Reply("%s", commands.FormatHelp(ce))
			return
		}
		if guide, ok := topicGuide(topic); ok {
			ce.Reply("%s", guide)
			return
		}
		ce.Reply("No guide for `%s` yet. Available guides: %s.\n\nSend `help` for the command list.", ce.Args[0], quotedTopics())
	},
	Name: "help",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionGeneral,
		Description: "Show the command list and the guides (`help <topic>`)",
	},
}
