// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"sort"
	"strings"

	"maunium.net/go/mautrix/bridgev2"
)

// setGhostCardDAVName records a CardDAV-sourced display name on the ghost.
// The name is kept in its own field so it never overwrites explicit custom
// names or XMPP roster names, and so it can be cleared independently. It
// returns true when an explicit custom name takes precedence over the CardDAV
// name (the CardDAV name is still stored, but does not win the lookup).
func (nc *MyNetworkClient) setGhostCardDAVName(ctx context.Context, jid, name string) bool {
	if nc == nil || nc.bridge == nil {
		return false
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	jid = CanonicalBareJID(jid)
	if jid == "" {
		return false
	}
	ghost, err := nc.bridge.GetGhostByID(ctx, MakeUserID(jid))
	if err != nil {
		nc.log.Warn().Err(err).Str("jid", jid).Msg("Failed to load ghost for CardDAV name")
		return false
	}
	meta, _ := ghost.Metadata.(*GhostMetadata)
	if meta == nil {
		meta = &GhostMetadata{}
	}
	conflict := strings.TrimSpace(meta.CustomName) != "" && meta.CustomName != name
	if strings.TrimSpace(meta.CardDAVName) == name {
		return conflict
	}
	meta.CardDAVName = name
	ghost.Metadata = meta
	if err := nc.bridge.DB.Ghost.Update(ctx, ghost.Ghost); err != nil {
		nc.log.Warn().Err(err).Str("jid", jid).Msg("Failed to save CardDAV name")
	}
	return conflict
}

// parseContactNameSource maps command-line source names to internal
// identifiers. An empty string means "all sources".
func parseContactNameSource(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return "", true
	case "explicit", "custom", "customname", "set-contact-name":
		return "custom", true
	case "carddav":
		return "carddav", true
	case "roster", "xmpp":
		return "roster", true
	default:
		return "", false
	}
}

// collectContactJIDs gathers the JIDs of all contacts that have portals for
// this login, including the participants of group portals.
func collectContactJIDs(ctx context.Context, bridge *bridgev2.Bridge, login *bridgev2.UserLogin) []string {
	if bridge == nil || login == nil {
		return nil
	}
	rows, err := bridge.DB.UserPortal.GetAllForLogin(ctx, login.UserLogin)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var jids []string
	add := func(jid string) {
		jid = CanonicalBareJID(jid)
		if jid == "" || seen[jid] {
			return
		}
		seen[jid] = true
		jids = append(jids, jid)
	}
	for _, row := range rows {
		if row == nil {
			continue
		}
		portal, err := bridge.GetExistingPortalByKey(ctx, row.Portal)
		if err != nil || portal == nil {
			continue
		}
		if contacts := portalContacts(portal); len(contacts) > 0 {
			for _, contact := range contacts {
				add(contact)
			}
			continue
		}
		jid := ""
		if meta, ok := portal.Metadata.(*PortalMetadata); ok && meta != nil && meta.OtherUserID != "" {
			jid = UnescapeJID(string(meta.OtherUserID))
		} else {
			jid = UnescapeJID(string(portal.ID))
		}
		add(jid)
	}
	sort.Strings(jids)
	return jids
}

// clearContactNameSources clears one source (or all sources when empty) of
// contact display names and refreshes auto-named rooms afterwards.
func clearContactNameSources(ctx context.Context, bridge *bridgev2.Bridge, login *bridgev2.UserLogin, source, jid, suffix string) (int, error) {
	if bridge == nil || login == nil {
		return 0, nil
	}
	var jids []string
	if jid != "" {
		if canonical := CanonicalBareJID(jid); canonical != "" {
			jids = []string{canonical}
		}
	} else {
		jids = collectContactJIDs(ctx, bridge, login)
	}
	cleared := 0
	for _, contactJID := range jids {
		ghost, err := bridge.GetGhostByID(ctx, MakeUserID(contactJID))
		if err != nil {
			continue
		}
		meta, _ := ghost.Metadata.(*GhostMetadata)
		if meta == nil {
			continue
		}
		changed := false
		clear := func(field *string) {
			if strings.TrimSpace(*field) != "" {
				*field = ""
				changed = true
			}
		}
		switch source {
		case "custom":
			clear(&meta.CustomName)
		case "carddav":
			clear(&meta.CardDAVName)
		case "roster":
			clear(&meta.RemoteName)
		default:
			clear(&meta.CustomName)
			clear(&meta.CardDAVName)
			clear(&meta.RemoteName)
		}
		if !changed {
			continue
		}
		ghost.Metadata = meta
		if err := bridge.DB.Ghost.Update(ctx, ghost.Ghost); err != nil {
			continue
		}
		cleared++
	}
	if cleared > 0 {
		_, _ = refreshAutoNamedPortals(ctx, bridge, login, suffix)
	}
	return cleared, nil
}

// countContactNameSources counts contacts by which name sources are set.
func countContactNameSources(ctx context.Context, bridge *bridgev2.Bridge, login *bridgev2.UserLogin) (custom, carddav, roster int) {
	if bridge == nil || login == nil {
		return 0, 0, 0
	}
	for _, jid := range collectContactJIDs(ctx, bridge, login) {
		ghost, err := bridge.GetExistingGhostByID(ctx, MakeUserID(jid))
		if err != nil || ghost == nil {
			continue
		}
		meta, _ := ghost.Metadata.(*GhostMetadata)
		if meta == nil {
			continue
		}
		if strings.TrimSpace(meta.CustomName) != "" {
			custom++
		}
		if strings.TrimSpace(meta.CardDAVName) != "" {
			carddav++
		}
		if strings.TrimSpace(meta.RemoteName) != "" {
			roster++
		}
	}
	return custom, carddav, roster
}
