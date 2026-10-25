// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"fmt"
	"time"

	"gosrc.io/xmpp/stanza"
)

const (
	mucHealthTickInterval = 15 * time.Second
	mucJoinMinBackoff     = 30 * time.Second
	mucJoinMaxBackoff     = 10 * time.Minute
	mucJoinNotifyAttempts = 5
	mucSelfPingInterval   = 15 * time.Minute
	mucSelfPingTimeout    = 15 * time.Second
	mucAutoRejoinMaxRooms = 200
	mucAutoRejoinStagger  = 500 * time.Millisecond
)

// mucJoinHealthState tracks the join/health status of a single MUC room.
type mucJoinHealthState struct {
	// attempts counts consecutive join attempts that were not yet confirmed
	// by a self-presence.
	attempts int
	// nextRetry is the earliest time the next join attempt may be sent.
	nextRetry time.Time
	// lastPing is when the last XEP-0410 self-ping was sent.
	lastPing time.Time
}

// mucJoinBackoff returns the delay before the next join retry for the given
// attempt count: 30s, 1m, 2m, 4m, 8m, capped at 10m.
func mucJoinBackoff(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	backoff := mucJoinMinBackoff
	for i := 1; i < attempts; i++ {
		backoff *= 2
		if backoff >= mucJoinMaxBackoff {
			return mucJoinMaxBackoff
		}
	}
	return backoff
}

// startMUCHealthLoop starts the periodic MUC join health checker once per
// client. It re-joins rooms that never confirmed a join and self-pings rooms
// that did, so that silently dropped MUC sessions are detected and repaired.
func (nc *MyNetworkClient) startMUCHealthLoop() {
	nc.mucHealthMu.Lock()
	if nc.mucHealthStarted {
		nc.mucHealthMu.Unlock()
		return
	}
	nc.mucHealthStarted = true
	nc.mucHealthMu.Unlock()

	go func() {
		ticker := time.NewTicker(mucHealthTickInterval)
		defer ticker.Stop()
		for range ticker.C {
			nc.runMUCHealthCheck()
		}
	}()
}

// noteMUCJoinAttempt records that a join presence was sent for a room and
// schedules the next retry using the backoff ladder.
func (nc *MyNetworkClient) noteMUCJoinAttempt(roomJID string) {
	nc.mucHealthMu.Lock()
	if nc.mucHealth == nil {
		nc.mucHealth = map[string]*mucJoinHealthState{}
	}
	state := nc.mucHealth[roomJID]
	if state == nil {
		state = &mucJoinHealthState{}
		nc.mucHealth[roomJID] = state
	}
	state.attempts++
	state.nextRetry = time.Now().Add(mucJoinBackoff(state.attempts))
	attempts := state.attempts
	nc.mucHealthMu.Unlock()

	if attempts == mucJoinNotifyAttempts {
		nc.notifyManagement(fmt.Sprintf("Still trying to join room %s after %d attempts.", roomJID, attempts))
	}
}

// markMUCJoined records a confirmed self-presence for a room.
func (nc *MyNetworkClient) markMUCJoined(roomJID string) {
	nc.mucHealthMu.Lock()
	defer nc.mucHealthMu.Unlock()
	if nc.mucHealth == nil {
		nc.mucHealth = map[string]*mucJoinHealthState{}
	}
	state := nc.mucHealth[roomJID]
	if state == nil {
		state = &mucJoinHealthState{}
		nc.mucHealth[roomJID] = state
	}
	state.attempts = 0
	state.nextRetry = time.Time{}
	state.lastPing = time.Time{}
	if nc.mucLeft != nil {
		delete(nc.mucLeft, roomJID)
	}
}

// markMUCUnjoined clears the joined flag (e.g. after a failed self-ping or an
// unavailable self-presence) and schedules a prompt rejoin.
func (nc *MyNetworkClient) markMUCUnjoined(roomJID string) {
	nc.mucHealthMu.Lock()
	defer nc.mucHealthMu.Unlock()
	if nc.mucJoined != nil {
		delete(nc.mucJoined, roomJID)
	}
	if nc.mucHealth == nil {
		nc.mucHealth = map[string]*mucJoinHealthState{}
	}
	state := nc.mucHealth[roomJID]
	if state == nil {
		state = &mucJoinHealthState{}
		nc.mucHealth[roomJID] = state
	}
	state.lastPing = time.Time{}
	state.nextRetry = time.Now().Add(mucJoinMinBackoff)
}

// runMUCHealthCheck rejoins rooms that are missing a confirmed join and
// periodically self-pings (XEP-0410) rooms that are joined.
func (nc *MyNetworkClient) runMUCHealthCheck() {
	if !nc.isXMPPConnected() {
		return
	}
	now := time.Now()

	var pings []string
	var joins []string
	nc.mucHealthMu.Lock()
	if nc.mucHealth == nil {
		nc.mucHealth = map[string]*mucJoinHealthState{}
	}
	for roomJID := range nc.mucRooms {
		if nc.mucLeft != nil && nc.mucLeft[roomJID] {
			continue
		}
		state := nc.mucHealth[roomJID]
		if state == nil {
			state = &mucJoinHealthState{}
			nc.mucHealth[roomJID] = state
		}
		if nc.mucJoined != nil && nc.mucJoined[roomJID] {
			if state.lastPing.IsZero() {
				state.lastPing = now
				continue
			}
			if now.Sub(state.lastPing) < mucSelfPingInterval {
				continue
			}
			state.lastPing = now
			pings = append(pings, roomJID)
			continue
		}
		if !state.nextRetry.IsZero() && now.Before(state.nextRetry) {
			continue
		}
		joins = append(joins, roomJID)
	}
	nc.mucHealthMu.Unlock()

	for _, roomJID := range pings {
		go nc.pingMUCSelf(roomJID, nc.mucNickOr(roomJID))
	}
	for _, roomJID := range joins {
		if nc.mucJoined != nil && nc.mucJoined[roomJID] {
			continue
		}
		nc.log.Debug().Str("room_jid", roomJID).Msg("Rejoining MUC room after missing self-presence")
		nc.joinMUC(roomJID, "", nc.mucPasswordFor(roomJID))
	}
}

// pingMUCSelf sends an XEP-0410 self-ping (an IQ get to our own occupant JID)
// and marks the room as not joined when it fails, so the health loop rejoins.
func (nc *MyNetworkClient) pingMUCSelf(roomJID, nick string) {
	if nc.xmppClient == nil || !nc.isXMPPConnected() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), mucSelfPingTimeout)
	defer cancel()
	iq, _ := stanza.NewIQ(stanza.Attrs{
		Type: stanza.IQTypeGet,
		To:   roomJID + "/" + nick,
		Id:   fmt.Sprintf("muc_self_ping_%d", time.Now().UnixNano()),
	})
	ch, err := nc.xmppClient.SendIQ(ctx, iq)
	if err != nil {
		nc.log.Warn().Err(err).Str("room_jid", roomJID).Msg("Failed to send MUC self-ping; rejoining")
		nc.markMUCUnjoined(roomJID)
		return
	}
	select {
	case resp := <-ch:
		if resp.Type == stanza.IQTypeResult {
			nc.log.Debug().Str("room_jid", roomJID).Msg("MUC self-ping succeeded")
			return
		}
		nc.log.Warn().Str("room_jid", roomJID).Str("type", string(resp.Type)).Msg("MUC self-ping returned non-result; rejoining")
		nc.markMUCUnjoined(roomJID)
	case <-ctx.Done():
		nc.log.Warn().Str("room_jid", roomJID).Msg("MUC self-ping timed out; rejoining")
		nc.markMUCUnjoined(roomJID)
	}
}

// scheduleMUCAutoRejoin rejoins rooms that were joined during a previous
// session but are not covered by an autojoin bookmark. It waits a few seconds
// for bookmarks to arrive first and staggers the joins.
func (nc *MyNetworkClient) scheduleMUCAutoRejoin() {
	go func() {
		time.Sleep(3 * time.Second)
		rooms := make([]string, 0, len(nc.mucRooms))
		for roomJID := range nc.mucRooms {
			if nc.mucLeft != nil && nc.mucLeft[roomJID] {
				continue
			}
			if nc.mucJoined != nil && nc.mucJoined[roomJID] {
				continue
			}
			rooms = append(rooms, roomJID)
			if len(rooms) >= mucAutoRejoinMaxRooms {
				break
			}
		}
		for i, roomJID := range rooms {
			if i > 0 {
				time.Sleep(mucAutoRejoinStagger)
			}
			if nc.mucJoined != nil && nc.mucJoined[roomJID] {
				continue
			}
			nc.joinMUC(roomJID, "", nc.mucPasswordFor(roomJID))
		}
	}()
}

// leaveMUC leaves a room and remembers that it should not be rejoined
// automatically (e.g. after its bookmark was retracted).
func (nc *MyNetworkClient) leaveMUC(roomJID string) {
	nick := nc.mucNickOr(roomJID)
	pres := stanza.NewPresence(stanza.Attrs{
		To:   roomJID + "/" + nick,
		Type: stanza.PresenceTypeUnavailable,
	})
	if err := nc.sendXMPP(pres); err != nil {
		nc.log.Err(err).Str("room_jid", roomJID).Msg("Failed to send MUC leave presence")
	}
	if nc.mucJoined != nil {
		delete(nc.mucJoined, roomJID)
	}
	if nc.mucLeft == nil {
		nc.mucLeft = map[string]bool{}
	}
	nc.mucLeft[roomJID] = true
	nc.log.Info().Str("room_jid", roomJID).Msg("Left MUC room")
}

// rejoinMUC clears the "intentionally left" flag for a room, e.g. after its
// bookmark reappeared with autojoin enabled.
func (nc *MyNetworkClient) rejoinMUC(roomJID string) {
	if nc.mucLeft != nil {
		delete(nc.mucLeft, roomJID)
	}
}

// mucPasswordFor returns the stored password for a MUC room, if any.
func (nc *MyNetworkClient) mucPasswordFor(roomJID string) string {
	if nc.mucPasswords == nil {
		return ""
	}
	return nc.mucPasswords[roomJID]
}
