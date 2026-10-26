// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	// mucInviteRateLimit is the maximum number of MUC invites per window that
	// the bridge acts on by creating a portal and joining the room.
	mucInviteRateLimit = 10
	// mucInviteRateWindow is the rolling window for the invite rate limit.
	mucInviteRateWindow = time.Hour
)

// mucInviteLimiter tracks recent MUC invites so a burst cannot auto-join an
// unbounded number of rooms.
type mucInviteLimiter struct {
	mu     sync.Mutex
	window []time.Time
}

// allow records an invite at now and reports whether it is within the limit.
func (l *mucInviteLimiter) allow(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-mucInviteRateWindow)
	kept := l.window[:0]
	for _, ts := range l.window {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	l.window = kept
	if len(l.window) >= mucInviteRateLimit {
		return false
	}
	l.window = append(l.window, now)
	return true
}

// allowMUCInvite records a MUC invite and reports whether it should be acted
// on. Invites beyond the rate limit are reported to the management room.
func (nc *MyNetworkClient) allowMUCInvite(roomJID, inviter, reason string) bool {
	if nc.mucInviteLimiter.allow(time.Now()) {
		return true
	}
	from := strings.TrimSpace(inviter)
	if from == "" {
		from = "unknown sender"
	}
	nc.log.Warn().
		Str("room_jid", roomJID).
		Str("inviter", from).
		Str("reason", reason).
		Msg("Ignoring MUC invite due to rate limit")
	nc.notifyManagement(fmt.Sprintf(
		"Ignoring MUC invite to %s from %s: more than %d invites were received in the last hour.",
		roomJID, from, mucInviteRateLimit,
	))
	return false
}
