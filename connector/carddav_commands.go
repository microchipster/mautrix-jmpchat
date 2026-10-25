// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"fmt"
	"strings"

	"maunium.net/go/mautrix/bridgev2/commands"
)

func cardDAVClientFromEvent(ce *commands.Event) *MyNetworkClient {
	login := ce.User.GetDefaultLogin()
	if login == nil {
		ce.Reply("No login found")
		return nil
	}
	client, ok := login.Client.(*MyNetworkClient)
	if !ok || client == nil {
		ce.Reply("Network client not available")
		return nil
	}
	return client
}

var CommandCardDAVLogin = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		if len(ce.Args) < 3 {
			ce.Reply("Usage: carddav-login <addressbook URL> <username> <password>")
			return
		}
		client := cardDAVClientFromEvent(ce)
		if client == nil {
			return
		}
		creds := &CardDAVCredentials{
			URL:      strings.TrimSpace(ce.Args[0]),
			Username: strings.TrimSpace(ce.Args[1]),
			Password: strings.TrimSpace(strings.Join(ce.Args[2:], " ")),
		}
		if err := validateCardDAVURL(client.cardDAVConfig(), creds.URL); err != nil {
			ce.Reply("Invalid CardDAV URL: %v", err)
			return
		}
		ctx, cancel := context.WithTimeout(ce.Ctx, cardDAVFetchTimeout)
		defer cancel()
		if err := client.setCardDAVCredentials(ctx, creds); err != nil {
			ce.Reply("Failed to store CardDAV credentials: %v", err)
			return
		}
		ce.Reply("CardDAV credentials stored for %s. Run `carddav-sync` to preview, or `carddav-sync apply` to import contacts into the roster.", creds.URL)
	},
	Name: "carddav-login",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionAuth,
		Description: "Store CardDAV addressbook URL and credentials for contact import",
	},
	RequiresLogin: true,
}

var CommandCardDAVLogout = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		client := cardDAVClientFromEvent(ce)
		if client == nil {
			return
		}
		creds := client.cardDAVCredentials()
		if creds == nil {
			ce.Reply("CardDAV is not configured")
			return
		}
		if err := client.clearCardDAVCredentials(ce.Ctx); err != nil {
			ce.Reply("Failed to clear CardDAV credentials: %v", err)
			return
		}
		ce.Reply("CardDAV credentials removed")
	},
	Name: "carddav-logout",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionAuth,
		Description: "Remove stored CardDAV credentials",
	},
	RequiresLogin: true,
}

var CommandCardDAVStatus = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		client := cardDAVClientFromEvent(ce)
		if client == nil {
			return
		}
		cfg := client.cardDAVConfig()
		lines := []string{"CardDAV configuration:"}
		lines = append(lines, fmt.Sprintf("- default country code: +%s", strings.TrimLeft(cfg.DefaultCountryCode, "+")))
		lines = append(lines, fmt.Sprintf("- allow insecure HTTP: %t", cfg.AllowInsecureHTTP))
		lines = append(lines, fmt.Sprintf("- allow private hosts: %t", cfg.AllowPrivateHosts))
		creds := client.cardDAVCredentials()
		if creds == nil || strings.TrimSpace(creds.URL) == "" {
			lines = append(lines, "- credentials: not configured (run `carddav-login <url> <username> <password>`)")
		} else {
			lines = append(lines, fmt.Sprintf("- addressbook: %s", creds.URL))
			lines = append(lines, fmt.Sprintf("- username: %s", creds.Username))
			if creds.LastSyncAt != nil {
				lines = append(lines, fmt.Sprintf("- last sync: %s (%d added)", creds.LastSyncAt.Format("2006-01-02 15:04:05 MST"), creds.LastSyncCount))
			} else {
				lines = append(lines, "- last sync: never")
			}
		}
		lines = append(lines, fmt.Sprintf("- roster contacts: %d", len(client.rosterContactList())))
		if login := ce.User.GetDefaultLogin(); login != nil {
			custom, carddav, roster := countContactNameSources(ce.Ctx, ce.Bridge, login)
			lines = append(lines, fmt.Sprintf("- name sources: %d explicit, %d CardDAV, %d roster", custom, carddav, roster))
		}
		ce.Reply("%s", strings.Join(lines, "\n"))
	},
	Name: "carddav-status",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionAuth,
		Description: "Show CardDAV import configuration and last sync state",
	},
	RequiresLogin: true,
}

var CommandCardDAVSync = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		client := cardDAVClientFromEvent(ce)
		if client == nil {
			return
		}
		apply := false
		if len(ce.Args) > 0 {
			switch strings.ToLower(ce.Args[0]) {
			case "apply", "--apply":
				apply = true
			case "dry", "dry-run", "--dry-run":
			default:
				ce.Reply("Usage: carddav-sync [apply]")
				return
			}
		}
		result, err := client.syncCardDAV(ce.Ctx, apply)
		if err != nil {
			ce.Reply("CardDAV sync failed: %v", err)
			return
		}
		if result.DryRun {
			ce.Reply("CardDAV dry run: %d contacts, %d phone numbers, %d already in roster, %d would be added. Run `carddav-sync apply` to import them.",
				result.Contacts, result.Numbers, result.Skipped, result.Candidates)
			return
		}
		ce.Reply("CardDAV sync: %d contacts, %d phone numbers, %d already in roster, %d added.",
			result.Contacts, result.Numbers, result.Skipped, result.Added)
	},
	Name: "carddav-sync",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionChats,
		Description: "Import phone numbers from the configured CardDAV addressbook into the roster",
	},
	RequiresLogin: true,
}

var CommandCardDAVClear = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		client := cardDAVClientFromEvent(ce)
		if client == nil {
			return
		}
		login := ce.User.GetDefaultLogin()
		if login == nil {
			ce.Reply("No login found")
			return
		}
		suffix := defaultContactNameSuffix
		if client.contactNameSuffix() != "" {
			suffix = client.contactNameSuffix()
		}
		count, err := clearContactNameSources(ce.Ctx, ce.Bridge, login, "carddav", "", suffix)
		if err != nil {
			ce.Reply("Failed to clear CardDAV names: %v", err)
			return
		}
		ce.Reply("Cleared CardDAV names for %d contacts. Auto-named rooms were refreshed to fall back to roster or phone-number names.", count)
	},
	Name: "carddav-clear",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionChats,
		Description: "Clear all CardDAV-imported display names and refresh auto-named rooms",
	},
	RequiresLogin: true,
}

var CommandClearContactNames = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		client := cardDAVClientFromEvent(ce)
		if client == nil {
			return
		}
		login := ce.User.GetDefaultLogin()
		if login == nil {
			ce.Reply("No login found")
			return
		}
		source := ""
		jid := ""
		for i := 0; i < len(ce.Args); i++ {
			arg := ce.Args[i]
			switch {
			case arg == "--source":
				if i+1 >= len(ce.Args) {
					ce.Reply("Usage: clear-contact-names [--source explicit|carddav|roster] [<phone or JID>]")
					return
				}
				i++
				parsed, ok := parseContactNameSource(ce.Args[i])
				if !ok {
					ce.Reply("Unknown name source %q (use explicit, carddav, or roster)", ce.Args[i])
					return
				}
				source = parsed
			case strings.HasPrefix(arg, "--source="):
				parsed, ok := parseContactNameSource(strings.TrimPrefix(arg, "--source="))
				if !ok {
					ce.Reply("Unknown name source %q (use explicit, carddav, or roster)", strings.TrimPrefix(arg, "--source="))
					return
				}
				source = parsed
			case strings.HasPrefix(arg, "-"):
				ce.Reply("Usage: clear-contact-names [--source explicit|carddav|roster] [<phone or JID>]")
				return
			default:
				normalized := normalizeJID(arg)
				if normalized == "" {
					ce.Reply("Invalid JID %q", arg)
					return
				}
				jid = normalized
			}
		}
		suffix := defaultContactNameSuffix
		if client.contactNameSuffix() != "" {
			suffix = client.contactNameSuffix()
		}
		count, err := clearContactNameSources(ce.Ctx, ce.Bridge, login, source, jid, suffix)
		if err != nil {
			ce.Reply("Failed to clear contact names: %v", err)
			return
		}
		if jid != "" {
			ce.Reply("Cleared %d name source(s) for %s", count, jid)
		} else {
			ce.Reply("Cleared %d name source(s) across contacts. Auto-named rooms were refreshed.", count)
		}
	},
	Name: "clear-contact-names",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionChats,
		Description: "Clear contact names from one source (explicit, carddav, roster) or all sources",
	},
	RequiresLogin: true,
}
