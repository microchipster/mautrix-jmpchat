// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"strings"
	"time"
)

// updateRosterContactName stores an XMPP roster contact name on the ghost as
// GhostMetadata.RemoteName. Explicit user-set names (CustomName, e.g. via the
// set-contact-name management command) always win and are never overwritten by
// roster data. It returns true when a name was newly stored or changed.
func (nc *MyNetworkClient) updateRosterContactName(jid, name string) bool {
	jid = CanonicalBareJID(jid)
	name = strings.TrimSpace(name)
	if jid == "" || name == "" || nc.bridge == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ghost, err := nc.bridge.GetGhostByID(ctx, MakeUserID(jid))
	if err != nil || ghost == nil {
		nc.log.Debug().Err(err).Str("jid", jid).Msg("Failed to load ghost for roster contact name")
		return false
	}
	meta, _ := ghost.Metadata.(*GhostMetadata)
	if meta == nil {
		meta = &GhostMetadata{}
	}
	if strings.TrimSpace(meta.CustomName) != "" || meta.RemoteName == name {
		return false
	}
	meta.RemoteName = name
	ghost.Metadata = meta
	if err := nc.bridge.DB.Ghost.Update(ctx, ghost.Ghost); err != nil {
		nc.log.Warn().Err(err).Str("jid", jid).Msg("Failed to store roster contact name")
		return false
	}
	return true
}

// refreshRosterNamedPortals regenerates auto-named rooms (DM phone-number
// titles and group initials) after roster names were imported, so rooms no
// longer keep stale names like a bare phone number.
func (nc *MyNetworkClient) refreshRosterNamedPortals() {
	if nc.bridge == nil || nc.login == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		changed, err := refreshAutoNamedPortals(ctx, nc.bridge, nc.login, nc.contactNameSuffix())
		if err != nil {
			nc.log.Warn().Err(err).Msg("Failed to refresh auto-named rooms after roster update")
			return
		}
		if changed > 0 {
			nc.log.Info().Int("count", changed).Msg("Refreshed auto-named rooms after roster update")
		}
	}()
}
