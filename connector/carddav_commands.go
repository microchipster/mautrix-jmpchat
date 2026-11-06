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
		if len(ce.Args) < 2 {
			ce.Reply("Usage: carddav-login <email or addressbook URL> [username] <password>")
			ce.Reply("Arguments are removed from the room after the command runs. With just an email address and password, the bridge tries the common CardDAV locations automatically; nothing is stored unless the connection test succeeds.")
			return
		}
		// Redact the message so the password is not kept in room history.
		ce.Redact()
		client := cardDAVClientFromEvent(ce)
		if client == nil {
			return
		}
		if client.cardDAVLocked() {
			ce.Reply("CardDAV is locked. Run `carddav-unlock <secret>` first to restore the in-memory key (nothing on disk can unlock it).")
			return
		}
		cfg := client.cardDAVConfig()
		var creds *CardDAVCredentials
		var data []byte
		if cardDAVLooksLikeURL(ce.Args[0]) {
			if len(ce.Args) < 3 {
				ce.Reply("Usage: carddav-login <addressbook URL> <username> <password>")
				return
			}
			creds = &CardDAVCredentials{
				URL:      strings.TrimSpace(ce.Args[0]),
				Username: strings.TrimSpace(ce.Args[1]),
				Password: strings.TrimSpace(strings.Join(ce.Args[2:], " ")),
			}
			if err := validateCardDAVURL(cfg, creds.URL); err != nil {
				ce.Reply("Invalid CardDAV URL: %v", err)
				return
			}
			ctx, cancel := context.WithTimeout(ce.Ctx, cardDAVFetchTimeout)
			defer cancel()
			// Test the connection before storing anything: a wrong password or
			// unreachable server must not leave broken credentials behind.
			var err error
			data, err = fetchCardDAVAddressbook(ctx, cfg, *creds)
			if err != nil {
				ce.Reply("CardDAV connection test failed: %v — credentials were not saved.", err)
				return
			}
		} else {
			hint := strings.TrimSpace(ce.Args[0])
			if !strings.Contains(hint, "@") {
				ce.Reply("That does not look like an email address. Use: carddav-login <email or addressbook URL> [username] <password>")
				return
			}
			username := hint
			password := ce.Args[1]
			if len(ce.Args) > 2 {
				username = strings.TrimSpace(ce.Args[1])
				password = strings.Join(ce.Args[2:], " ")
			}
			if strings.TrimSpace(password) == "" {
				ce.Reply("A password is required.")
				return
			}
			candidates := cardDAVCandidateURLs(hint, username)
			if len(candidates) == 0 {
				ce.Reply("Could not work out any address book URLs for %s. If you know the URL, use: carddav-login <addressbook URL> <username> <password>.", hint)
				return
			}
			ctx, cancel := context.WithTimeout(ce.Ctx, cardDAVDiscoveryTimeout)
			defer cancel()
			ce.Reply("Looking for your CardDAV address book for %s (trying %d common addresses)…", hint, len(candidates))
			url, found, tried, err := discoverCardDAVAddressbook(ctx, cfg, candidates, username, password)
			if err != nil {
				ce.Reply("Could not find your CardDAV address book automatically (%d addresses tried). If you know the URL, use: carddav-login <addressbook URL> <username> <password>.", tried)
				return
			}
			ce.Reply("Found your address book: %s", url)
			creds = &CardDAVCredentials{URL: url, Username: username, Password: password}
			data = found
		}
		contacts := parseVCards(data)
		numbers := 0
		for _, contact := range contacts {
			for _, raw := range contact.Phones {
				if _, ok := normalizeCardDAVPhoneNumber(raw, cfg.DefaultCountryCode); ok {
					numbers++
				}
			}
		}
		if err := client.setCardDAVCredentials(ce.Ctx, creds); err != nil {
			ce.Reply("Failed to store CardDAV credentials: %v", err)
			return
		}
		ce.Reply("CardDAV connection test passed: %d contact(s) with %d phone number(s). Credentials stored for %s.\n\nRun `carddav-sync --no-apply` to preview the import, then `carddav-sync` to add the contacts to your roster and apply their names (and photos) to auto-named rooms.\n\n%s", len(contacts), numbers, creds.URL, client.cardDAVEncryptionNote())
	},
	Name: "carddav-login",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionAuth,
		Description: "Store CardDAV addressbook URL and credentials for contact import (an email address is enough; the URL is found automatically; the command message is redacted)",
		Args:        "[email or addressbook URL] [username] <password>",
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
		if login := ce.User.GetDefaultLogin(); login != nil {
			meta, _ := login.Metadata.(*LoginMetadata)
			switch {
			case client.cardDAVLocked():
				lines = append(lines, "Encryption: locked — run `carddav-unlock <secret>` (the key is kept in memory only)")
			case meta != nil && meta.CardDAVKeyMode == cardDAVKeyModeMemory:
				lines = append(lines, "Encryption: unlocked (in-memory key; not stored on disk)")
			default:
				if cardDAVLegacyKeyFileExists() {
					lines = append(lines, "Encryption: legacy key file (/data/carddav.key) — run `carddav-unlock <secret>` to switch to an in-memory key")
				} else {
					lines = append(lines, "Encryption: no key set (run `carddav-login` or `carddav-unlock <secret>`)")
				}
			}
		}
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

// parseCardDAVSyncArgs interprets carddav-sync flags. Importing is the default
// (the first successful import acts as the takeover), --no-apply/dry-run
// switches to a preview, and --force-names also refreshes rooms whose names
// were customized by hand.
func parseCardDAVSyncArgs(args []string) (apply, force, ok bool) {
	apply = true
	for _, arg := range args {
		switch strings.ToLower(strings.TrimSpace(arg)) {
		case "":
		case "apply", "--apply":
			apply = true
		case "dry", "dry-run", "--dry-run", "--no-apply":
			apply = false
		case "force-names", "--force-names":
			force = true
		default:
			return false, false, false
		}
	}
	return apply, force, true
}

var CommandCardDAVSync = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		client := cardDAVClientFromEvent(ce)
		if client == nil {
			return
		}
		if client.cardDAVLocked() {
			ce.Reply("CardDAV is locked. Run `carddav-unlock <secret>` first (the key is kept in memory only).")
			return
		}
		apply, force, ok := parseCardDAVSyncArgs(ce.Args)
		if !ok {
			ce.Reply("Usage: carddav-sync [--no-apply] [--force-names]")
			return
		}
		creds := client.cardDAVCredentials()
		firstApply := apply && (creds == nil || creds.LastSyncAt == nil)
		result, err := client.syncCardDAV(ce.Ctx, apply)
		if err != nil {
			ce.Reply("CardDAV sync failed: %v", err)
			return
		}
		if result.DryRun {
			ce.Reply("CardDAV dry run: %d contacts, %d phone numbers, %d already in roster, %d would be added. Run `carddav-sync` to import them.",
				result.Contacts, result.Numbers, result.Skipped, result.Candidates)
			return
		}
		lines := []string{fmt.Sprintf("CardDAV sync: %d contacts, %d phone numbers, %d already in roster, %d added.",
			result.Contacts, result.Numbers, result.Skipped, result.Added)}
		if result.Conflicts > 0 {
			lines = append(lines, fmt.Sprintf("%d contact(s) keep an explicit name from `set-contact-name`; use `clear-contact-names --source explicit` to let CardDAV names win.", result.Conflicts))
		}
		if firstApply {
			lines = append(lines, "CardDAV names now take precedence over roster names for auto-named rooms. Explicit `set-contact-name` entries are never overwritten; `carddav-clear` removes CardDAV names again.")
		}
		if force {
			login := ce.User.GetDefaultLogin()
			if login == nil {
				ce.Reply("No login found")
				return
			}
			suffix := defaultContactNameSuffix
			if client.contactNameSuffix() != "" {
				suffix = client.contactNameSuffix()
			}
			refreshed, refreshErr := refreshAllPortalNames(ce.Ctx, ce.Bridge, login, suffix)
			if refreshErr != nil {
				lines = append(lines, fmt.Sprintf("Forced room rename failed: %v", refreshErr))
			} else {
				lines = append(lines, fmt.Sprintf("Forced refresh queued room renames for %d room(s), including ones renamed by hand.", refreshed))
			}
		}
		ce.Reply("%s", strings.Join(lines, "\n"))
	},
	Name: "carddav-sync",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionChats,
		Description: "Import phone numbers and names from the configured CardDAV addressbook (--no-apply previews, --force-names also renames customized rooms)",
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

var CommandCardDAVUnlock = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		if len(ce.Args) == 0 {
			ce.Reply("Usage: carddav-unlock <secret>")
			ce.Reply("The secret derives the key that encrypts your CardDAV data; the key is kept in memory and never written to disk, so you can unlock again with the same secret after a restart. The command message is redacted. If the old key can no longer decrypt what is stored, run `carddav-reset yes`, `carddav-login` and `carddav-sync` to fetch the address book again. Tip: run this in an encrypted room.")
			return
		}
		// Redact the message so the secret is not kept in room history.
		ce.Redact()
		client := cardDAVClientFromEvent(ce)
		if client == nil {
			return
		}
		login := ce.User.GetDefaultLogin()
		if login == nil {
			ce.Reply("No login found")
			return
		}
		secret := strings.TrimSpace(strings.Join(ce.Args, " "))
		if secret == "" {
			ce.Reply("Usage: carddav-unlock <secret>")
			return
		}
		oldKey, _ := cardDAVProcessKey()
		newKey := cardDAVUnlockKeyFromSecret(secret)
		meta, _ := login.Metadata.(*LoginMetadata)
		if meta == nil {
			meta = &LoginMetadata{}
		}
		if meta.CardDAV != nil {
			staged := *meta.CardDAV
			if stored := strings.TrimSpace(staged.Password); stored != "" {
				if cardDAVNameIsEncrypted(stored) {
					if oldKey == nil {
						ce.Reply("The stored CardDAV password is encrypted with a key that is not available any more. Run `carddav-reset yes`, then `carddav-login` again to refetch from your CardDAV server.")
						return
					}
					plain, err := decryptCardDAVWithKey(oldKey, stored)
					if err != nil {
						ce.Reply("Failed to decrypt the stored CardDAV password: %v. Run `carddav-reset yes` and `carddav-login` again to refetch from your CardDAV server.", err)
						return
					}
					encrypted, err := encryptCardDAVWithKey(newKey, plain)
					if err != nil {
						ce.Reply("Failed to encrypt the stored CardDAV password: %v", err)
						return
					}
					staged.Password = encrypted
				} else {
					encrypted, err := encryptCardDAVWithKey(newKey, stored)
					if err != nil {
						ce.Reply("Failed to encrypt the stored CardDAV password: %v", err)
						return
					}
					staged.Password = encrypted
				}
			}
			meta.CardDAV = &staged
		}
		setCardDAVRAMKey(newKey)
		meta.CardDAVKeyMode = cardDAVKeyModeMemory
		login.Metadata = meta
		if err := login.Save(ce.Ctx); err != nil {
			ce.Reply("Failed to store the CardDAV unlock state: %v", err)
			return
		}
		note := ""
		if cardDAVLegacyKeyFileExists() {
			note = " The legacy key file /data/carddav.key is no longer needed and can be deleted."
		}
		if creds := client.cardDAVCredentials(); creds != nil && strings.TrimSpace(creds.URL) != "" {
			result, err := client.syncCardDAV(ce.Ctx, true)
			if err != nil {
				ce.Reply("CardDAV is unlocked. Refresh it later with `carddav-sync` (now: %v).%s", err, note)
				return
			}
			ce.Reply("CardDAV is unlocked and the address book was re-synced (%d updated, %d added).%s", result.Updated, result.Added, note)
			return
		}
		ce.Reply("CardDAV is unlocked. Run `carddav-login <email> <password>` to connect your address book.%s", note)
	},
	Name: "carddav-unlock",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionAuth,
		Description: "Derive the CardDAV encryption key from a secret and keep it in memory only",
	},
	RequiresLogin: true,
}

var CommandCardDAVLock = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		if cardDAVClientFromEvent(ce) == nil {
			return
		}
		clearCardDAVRAMKey()
		ce.Reply("CardDAV is locked: the in-memory key was forgotten and background syncing is paused. Run `carddav-unlock <secret>` with the same secret to unlock again; nothing is lost.")
	},
	Name: "carddav-lock",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionAuth,
		Description: "Forget the in-memory CardDAV key and pause syncing",
	},
	RequiresLogin: true,
}

var CommandCardDAVReset = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		if len(ce.Args) == 0 || strings.ToLower(strings.TrimSpace(ce.Args[0])) != "yes" {
			ce.Reply("Usage: carddav-reset yes")
			ce.Reply("This forgets the stored CardDAV credentials, removes all imported names and photos, and locks the encryption key. The address book stays on your CardDAV server: run `carddav-unlock <secret>`, `carddav-login <email> <password>` and `carddav-sync` to fetch it again.")
			return
		}
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
		if configured := client.contactNameSuffix(); configured != "" {
			suffix = configured
		}
		cleared, err := clearContactNameSources(ce.Ctx, ce.Bridge, login, "carddav", "", suffix)
		if err != nil {
			ce.Reply("Failed to remove imported names: %v", err)
			return
		}
		if err := client.clearCardDAVCredentials(ce.Ctx); err != nil {
			ce.Reply("Failed to remove the stored credentials: %v", err)
			return
		}
		meta, _ := login.Metadata.(*LoginMetadata)
		if meta != nil && meta.CardDAVKeyMode != "" {
			meta.CardDAVKeyMode = ""
			login.Metadata = meta
			if err := login.Save(ce.Ctx); err != nil {
				ce.Reply("Failed to clean the CardDAV unlock state: %v", err)
				return
			}
		}
		clearCardDAVRAMKey()
		ce.Reply("CardDAV was reset: credentials, imported names and photos removed (%d name(s)), and the encryption key forgotten. The address book still lives on your CardDAV server — run `carddav-unlock <secret>`, `carddav-login <email> <password>` and `carddav-sync` to fetch it again.", cleared)
	},
	Name: "carddav-reset",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionAuth,
		Description: "Remove all CardDAV data, credentials and imported names",
	},
	RequiresLogin: true,
}
