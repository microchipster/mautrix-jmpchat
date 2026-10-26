// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/id"
)

// fakeMessageIDPattern matches bridge-generated placeholder rows so
// interaction statistics only count real bridged messages.
const fakeMessageIDPattern = "~fake:%"

// portalInteractionStats summarizes one portal's bridged message history.
type portalInteractionStats struct {
	MessageCount int
	FirstTS      time.Time
	LastTS       time.Time
}

// getPortalInteractionStats counts real (non-fake) messages in a portal and
// finds the first and last timestamps. Nil-safe.
func getPortalInteractionStats(ctx context.Context, bridge *bridgev2.Bridge, portal *bridgev2.Portal) portalInteractionStats {
	var stats portalInteractionStats
	if bridge == nil || portal == nil {
		return stats
	}
	_ = bridge.DB.Message.GetDB().QueryRow(ctx,
		"SELECT COUNT(*) FROM message WHERE bridge_id=$1 AND room_id=$2 AND room_receiver=$3 AND mxid NOT LIKE '~fake:%'",
		portal.BridgeID, portal.ID, portal.Receiver).Scan(&stats.MessageCount)
	if firstMsg, err := bridge.DB.Message.GetFirstPortalMessageExcludingIDPattern(ctx, portal.PortalKey, fakeMessageIDPattern); err == nil && firstMsg != nil {
		stats.FirstTS = firstMsg.Timestamp
	}
	if lastMsg, err := bridge.DB.Message.GetLastNonFakePartAtOrBeforeTime(ctx, portal.PortalKey, time.Now()); err == nil && lastMsg != nil {
		stats.LastTS = lastMsg.Timestamp
	}
	return stats
}

// contactInteraction aggregates per-contact interaction statistics across
// every portal the login is part of.
type contactInteraction struct {
	JID      string
	Name     string
	Self     bool
	DM       bool
	Count    int
	FirstTS  time.Time
	LastTS   time.Time
	RoomID   id.RoomID
	RoomIDTS time.Time
}

func (ci *contactInteraction) merge(stats portalInteractionStats) {
	ci.Count += stats.MessageCount
	if !stats.FirstTS.IsZero() && (ci.FirstTS.IsZero() || stats.FirstTS.Before(ci.FirstTS)) {
		ci.FirstTS = stats.FirstTS
	}
	if stats.LastTS.After(ci.LastTS) {
		ci.LastTS = stats.LastTS
	}
}

// contactInteractionKey normalizes a JID for aggregation.
func contactInteractionKey(jid string) string {
	return strings.ToLower(bareJID(UnescapeJID(strings.TrimSpace(jid))))
}

// collectContactInteractions walks every portal of the login and aggregates
// per-contact message counts and first/last activity. The user's own JMP
// account is always included and annotated as such.
func collectContactInteractions(ctx context.Context, bridge *bridgev2.Bridge, login *bridgev2.UserLogin) []*contactInteraction {
	if bridge == nil || login == nil {
		return nil
	}
	ownJID := ""
	if meta, ok := login.Metadata.(*LoginMetadata); ok && meta != nil {
		ownJID = strings.TrimSpace(meta.RemoteUserID)
	}
	ownKey := ""
	if ownJID != "" {
		ownKey = contactInteractionKey(ownJID)
	}

	byJID := map[string]*contactInteraction{}
	get := func(jid string) *contactInteraction {
		key := contactInteractionKey(jid)
		if key == "" {
			return nil
		}
		ci, ok := byJID[key]
		if !ok {
			ci = &contactInteraction{JID: bareJID(jid)}
			if key == ownKey {
				ci.Self = true
				if ownJID != "" {
					ci.JID = ownJID
				}
			}
			byJID[key] = ci
		}
		return ci
	}

	rows, err := bridge.DB.UserPortal.GetAllForLogin(ctx, login.UserLogin)
	if err == nil {
		for _, row := range rows {
			portal, err := bridge.GetExistingPortalByKey(ctx, row.Portal)
			if err != nil || portal == nil || portal.MXID == "" {
				continue
			}
			if portal.RoomType == database.RoomTypeSpace {
				continue
			}
			stats := getPortalInteractionStats(ctx, bridge, portal)
			if portalUsesGroupChat(portal) {
				for _, contact := range portalContacts(portal) {
					if ci := get(contact); ci != nil {
						ci.merge(stats)
						if ci.RoomID == "" || stats.LastTS.After(ci.RoomIDTS) {
							ci.RoomID = portal.MXID
							ci.RoomIDTS = stats.LastTS
						}
					}
				}
				continue
			}
			if portal.RoomType != database.RoomTypeDM {
				continue
			}
			jid := ""
			if meta, ok := portal.Metadata.(*PortalMetadata); ok && meta != nil {
				jid = strings.TrimSpace(string(meta.OtherUserID))
			}
			if jid == "" {
				jid = UnescapeJID(string(portal.ID))
			}
			if ci := get(jid); ci != nil {
				ci.DM = true
				ci.merge(stats)
				if ci.RoomID == "" || stats.LastTS.After(ci.RoomIDTS) {
					ci.RoomID = portal.MXID
					ci.RoomIDTS = stats.LastTS
				}
			}
		}
	}

	if ownKey != "" {
		if ci, ok := byJID[ownKey]; ok {
			ci.Self = true
		} else {
			ci := get(ownJID)
			if ci != nil {
				ci.Self = true
			}
		}
	}

	res := make([]*contactInteraction, 0, len(byJID))
	for _, ci := range byJID {
		ci.Name = contactDisplayName(ctx, bridge, ci.JID)
		res = append(res, ci)
	}
	sort.Slice(res, func(i, j int) bool {
		if res[i].Self != res[j].Self {
			return res[i].Self
		}
		if !res[i].LastTS.Equal(res[j].LastTS) {
			return res[i].LastTS.After(res[j].LastTS)
		}
		if res[i].Count != res[j].Count {
			return res[i].Count > res[j].Count
		}
		return res[i].JID < res[j].JID
	})
	return res
}

// formatInteractionDate renders a timestamp for query output.
func formatInteractionDate(ts time.Time) string {
	if ts.IsZero() {
		return "never"
	}
	days := int(time.Since(ts).Hours() / 24)
	switch {
	case days <= 0:
		return fmt.Sprintf("today (%s)", ts.UTC().Format("2006-01-02"))
	case days == 1:
		return fmt.Sprintf("yesterday (%s)", ts.UTC().Format("2006-01-02"))
	default:
		return fmt.Sprintf("%d days ago (%s)", days, ts.UTC().Format("2006-01-02"))
	}
}

// formatFirstInteractionDate renders a first-contact timestamp as an
// absolute date for query output.
func formatFirstInteractionDate(ts time.Time) string {
	if ts.IsZero() {
		return "never"
	}
	return ts.UTC().Format("2006-01-02")
}

// formatInteractionSummary renders the shared "N msgs - first - last" tail.
func formatInteractionSummary(ci *contactInteraction) string {
	if ci.Count == 0 {
		return "no bridged messages yet"
	}
	first := "unknown"
	if !ci.FirstTS.IsZero() {
		first = ci.FirstTS.UTC().Format("2006-01-02")
	}
	last := "unknown"
	if !ci.LastTS.IsZero() {
		last = formatInteractionDate(ci.LastTS)
	}
	return fmt.Sprintf("%d msgs · first %s · last %s", ci.Count, first, last)
}

// formatContactInteractionLine renders one contact for list commands.
func formatContactInteractionLine(ci *contactInteraction) string {
	var b strings.Builder
	b.WriteString("- ")
	name := strings.TrimSpace(ci.Name)
	switch {
	case ci.Self:
		name = "You"
		fmt.Fprintf(&b, "**%s** `%s` — your own JMP account", name, ci.JID)
	case name != "" && !strings.EqualFold(name, ci.JID):
		fmt.Fprintf(&b, "**%s** `%s`", name, ci.JID)
	default:
		fmt.Fprintf(&b, "`%s`", ci.JID)
	}
	if !ci.Self {
		b.WriteString(" — ")
		b.WriteString(formatInteractionSummary(ci))
	} else {
		if summary := formatInteractionSummary(ci); summary != "no bridged messages yet" {
			b.WriteString(" · ")
			b.WriteString(summary)
		}
	}
	if ci.RoomID != "" {
		fmt.Fprintf(&b, " · [open room](%s)", ci.RoomID.URI().MatrixToURL())
	}
	return b.String()
}
