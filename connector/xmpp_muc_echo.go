// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"strings"
	"time"
)

const (
	mucEchoTTL    = 5 * time.Minute
	mucEchoWait   = 3 * time.Second
	mucEchoPoll   = 100 * time.Millisecond
	mucEchoMapCap = 1024
)

// mucEchoEntry remembers what the MUC room assigned to one of our own sent
// messages when it echoed the stanza back. XEP-0424 retractions in MUC rooms
// must reference the room-assigned stanza-id (or the occupant-id), not the
// origin-id we sent, so the echo has to be learned after the fact.
type mucEchoEntry struct {
	stanzaID   string
	occupantID string
	at         time.Time
}

// recordMucEcho stores the room-assigned identifiers for one of our own
// origin-ids. Empty values are ignored so a partial echo does not overwrite
// a previously learned stanza-id.
func (nc *MyNetworkClient) recordMucEcho(originID, stanzaID, occupantID string) {
	originID = strings.TrimSpace(originID)
	if originID == "" || nc.mucEchoes == nil {
		return
	}
	stanzaID = strings.TrimSpace(stanzaID)
	occupantID = strings.TrimSpace(occupantID)
	if stanzaID == "" && occupantID == "" {
		return
	}
	now := time.Now()
	nc.mucEchoMu.Lock()
	defer nc.mucEchoMu.Unlock()
	if len(nc.mucEchoes) >= mucEchoMapCap {
		nc.pruneMucEchoesLocked(now)
		if len(nc.mucEchoes) >= mucEchoMapCap {
			nc.evictOldestMucEchoLocked()
		}
	}
	existing := nc.mucEchoes[originID]
	if existing.stanzaID == "" {
		existing.stanzaID = stanzaID
	}
	if existing.occupantID == "" {
		existing.occupantID = occupantID
	}
	existing.at = now
	nc.mucEchoes[originID] = existing
}

// lookupMucEcho returns the identifiers learned for an origin-id, if any.
func (nc *MyNetworkClient) lookupMucEcho(originID string) (mucEchoEntry, bool) {
	originID = strings.TrimSpace(originID)
	if originID == "" || nc.mucEchoes == nil {
		return mucEchoEntry{}, false
	}
	nc.mucEchoMu.Lock()
	defer nc.mucEchoMu.Unlock()
	entry, ok := nc.mucEchoes[originID]
	if !ok {
		return mucEchoEntry{}, false
	}
	if time.Since(entry.at) > mucEchoTTL {
		delete(nc.mucEchoes, originID)
		return mucEchoEntry{}, false
	}
	return entry, true
}

// waitMucEcho waits up to timeout for the echo of one of our own MUC
// messages. It returns false when the room never echoed the stanza (for
// example because it does not support stanza-ids).
func (nc *MyNetworkClient) waitMucEcho(ctx context.Context, originID string, timeout time.Duration) (mucEchoEntry, bool) {
	if entry, ok := nc.lookupMucEcho(originID); ok {
		return entry, true
	}
	if strings.TrimSpace(originID) == "" || nc.mucEchoes == nil || timeout <= 0 {
		return mucEchoEntry{}, false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return mucEchoEntry{}, false
		}
		wait := mucEchoPoll
		if remaining < wait {
			wait = remaining
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return mucEchoEntry{}, false
		case <-timer.C:
		}
		if entry, ok := nc.lookupMucEcho(originID); ok {
			return entry, true
		}
	}
}

// pruneMucEchoesLocked drops expired entries. Callers must hold mucEchoMu.
func (nc *MyNetworkClient) pruneMucEchoesLocked(now time.Time) {
	for originID, entry := range nc.mucEchoes {
		if now.Sub(entry.at) > mucEchoTTL {
			delete(nc.mucEchoes, originID)
		}
	}
}

// evictOldestMucEchoLocked removes the oldest entry so the table never grows
// past its cap even when nothing has expired yet. Callers must hold mucEchoMu.
func (nc *MyNetworkClient) evictOldestMucEchoLocked() {
	oldestID := ""
	var oldestAt time.Time
	for originID, entry := range nc.mucEchoes {
		if oldestID == "" || entry.at.Before(oldestAt) {
			oldestID = originID
			oldestAt = entry.at
		}
	}
	if oldestID != "" {
		delete(nc.mucEchoes, oldestID)
	}
}
