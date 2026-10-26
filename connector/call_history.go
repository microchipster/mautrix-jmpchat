// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/commands"
)

const callHistoryLimit = 50

// recordCallHistory prepends an entry to the login's bounded call log.
// It is best-effort: failures are logged, never returned.
func recordCallHistory(ctx context.Context, login *bridgev2.UserLogin, entry CallHistoryEntry) {
	if login == nil {
		return
	}
	entry.Number = strings.TrimSpace(entry.Number)
	if entry.Number == "" {
		return
	}
	if entry.StartedAt.IsZero() {
		entry.StartedAt = time.Now()
	}
	meta, ok := login.Metadata.(*LoginMetadata)
	if !ok || meta == nil {
		return
	}
	log := login.Log
	meta.CallHistory = append([]CallHistoryEntry{entry}, meta.CallHistory...)
	if len(meta.CallHistory) > callHistoryLimit {
		meta.CallHistory = meta.CallHistory[:callHistoryLimit]
	}
	login.Metadata = meta
	if err := login.Save(ctx); err != nil {
		log.Warn().Err(err).Msg("Failed to persist call history")
	}
}

// formatHistoryDuration renders a call duration in a compact form.
func formatHistoryDuration(seconds int) string {
	if seconds <= 0 {
		return ""
	}
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	return fmt.Sprintf("%dm%02ds", seconds/60, seconds%60)
}

// formatCallHistoryLine renders one call history entry as a markdown list line.
func formatCallHistoryLine(entry CallHistoryEntry) string {
	parts := []string{formatInteractionDate(entry.StartedAt)}
	if entry.Direction != "" {
		parts = append(parts, entry.Direction)
	}
	if d := formatHistoryDuration(entry.DurationSeconds); d != "" {
		parts = append(parts, "duration "+d)
	}
	if status := strings.TrimSpace(entry.Status); status != "" {
		parts = append(parts, status)
	}
	return fmt.Sprintf("- **%s** — %s", entry.Number, strings.Join(parts, " · "))
}

// CommandCallHistory lists recent calls recorded for this login.
var CommandCallHistory = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		login := ce.User.GetDefaultLogin()
		if login == nil {
			ce.Reply("No login found.")
			return
		}
		meta, _ := login.Metadata.(*LoginMetadata)
		if meta == nil || len(meta.CallHistory) == 0 {
			ce.Reply("No calls recorded yet.")
			return
		}
		limit := len(meta.CallHistory)
		if len(ce.Args) > 0 {
			if n, err := strconv.Atoi(ce.Args[0]); err == nil && n > 0 && n < limit {
				limit = n
			}
		}
		lines := make([]string, 0, limit)
		for _, entry := range meta.CallHistory[:limit] {
			lines = append(lines, formatCallHistoryLine(entry))
		}
		ce.Reply("Recent calls (%d):\n%s", limit, strings.Join(lines, "\n"))
	},
	Name:          "call-history",
	Aliases:       []string{"calls"},
	Help:          commands.HelpMeta{Section: commands.HelpSectionChats, Description: "List recent calls (incoming and outgoing)"},
	RequiresLogin: true,
}
