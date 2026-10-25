// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gosrc.io/xmpp/stanza"
)

// maxMUCNickAlternatives is how many "_1".."_3" style alternatives are tried
// when the preferred nickname is already in use.
const maxMUCNickAlternatives = 3

// deriveMUCNickAlternatives returns the fallback nicknames for a base nickname,
// e.g. base "matrix" with max 3 becomes ["matrix_1", "matrix_2", "matrix_3"].
func deriveMUCNickAlternatives(base string, max int) []string {
	base = strings.TrimSpace(base)
	if base == "" || max <= 0 {
		return nil
	}
	alternatives := make([]string, 0, max)
	for i := 1; i <= max; i++ {
		alternatives = append(alternatives, fmt.Sprintf("%s_%d", base, i))
	}
	return alternatives
}

// isNickConflictPresence reports whether a MUC presence error means the
// requested nickname is already taken (XEP-0045 code 409).
func isNickConflictPresence(pres stanza.Presence) bool {
	if pres.Type != stanza.PresenceTypeError {
		return false
	}
	if pres.Error.Code == 409 {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(pres.Error.Reason), "conflict")
}

// mucNickOverride returns the persisted nickname override for a room, if any.
func (nc *MyNetworkClient) mucNickOverride(roomJID string) string {
	if nc.login == nil {
		return ""
	}
	meta, _ := nc.login.Metadata.(*LoginMetadata)
	if meta == nil || len(meta.MUCNickOverrides) == 0 {
		return ""
	}
	return strings.TrimSpace(meta.MUCNickOverrides[roomJID])
}

// mucNickForRoom resolves the nickname to use when joining a room. An explicit
// override (from a nickname conflict or the muc-nick command) wins over the
// bookmark nickname, which wins over the account default.
func (nc *MyNetworkClient) mucNickForRoom(roomJID, bookmarkNick string) string {
	if override := nc.mucNickOverride(roomJID); override != "" {
		return override
	}
	if nick := strings.TrimSpace(bookmarkNick); nick != "" {
		return nick
	}
	return nc.defaultMUCNick()
}

// mucNickOr returns the nickname currently used in the room, falling back to
// the configured override or the account default.
func (nc *MyNetworkClient) mucNickOr(roomJID string) string {
	if nc.mucNick != nil {
		if nick := strings.TrimSpace(nc.mucNick[roomJID]); nick != "" {
			return nick
		}
	}
	return nc.mucNickForRoom(roomJID, "")
}

// setMUCNickOverride persists (or clears, when nick is empty) the nickname
// override for a room in the login metadata.
func (nc *MyNetworkClient) setMUCNickOverride(ctx context.Context, roomJID, nick string) error {
	if nc.login == nil {
		return fmt.Errorf("login is not available")
	}
	meta, _ := nc.login.Metadata.(*LoginMetadata)
	if meta == nil {
		meta = &LoginMetadata{}
	}
	if meta.MUCNickOverrides == nil {
		meta.MUCNickOverrides = map[string]string{}
	}
	nick = strings.TrimSpace(nick)
	if nick == "" {
		delete(meta.MUCNickOverrides, roomJID)
	} else {
		meta.MUCNickOverrides[roomJID] = nick
	}
	nc.login.Metadata = meta
	return nc.login.Save(ctx)
}

// sendMUCNickChange updates the remembered nickname and asks the MUC service
// to change our occupant nickname in the room.
func (nc *MyNetworkClient) sendMUCNickChange(roomJID, nick string) {
	if nc.mucNick == nil {
		nc.mucNick = map[string]string{}
	}
	nc.mucNick[roomJID] = nick
	pres := stanza.NewPresence(stanza.Attrs{To: roomJID + "/" + nick})
	if err := nc.sendXMPP(pres); err != nil {
		nc.log.Err(err).Str("room_jid", roomJID).Str("nick", nick).Msg("Failed to send MUC nickname change presence")
	}
}

// handleMUCPresenceError reacts to error presences from MUC rooms. When the
// preferred nickname is taken, the next alternative is tried and, once a join
// is confirmed, remembered as the room's nickname.
func (nc *MyNetworkClient) handleMUCPresenceError(roomJID, resource string, pres stanza.Presence) {
	if !isNickConflictPresence(pres) {
		nc.log.Warn().
			Str("room_jid", roomJID).
			Str("resource", resource).
			Int("code", pres.Error.Code).
			Str("reason", pres.Error.Reason).
			Msg("MUC presence error")
		return
	}

	base := nc.mucNickForRoom(roomJID, "")
	alternatives := deriveMUCNickAlternatives(base, maxMUCNickAlternatives)

	nc.mucNickAltMu.Lock()
	if nc.mucNickAltIndex == nil {
		nc.mucNickAltIndex = map[string]int{}
	}
	index := nc.mucNickAltIndex[roomJID]
	if index >= len(alternatives) {
		nc.mucNickAltMu.Unlock()
		nc.log.Warn().Str("room_jid", roomJID).Str("nick", base).Msg("Exhausted MUC nickname alternatives")
		nc.notifyManagement(fmt.Sprintf("Could not join room %s: nickname %q and its alternatives are all in use.", roomJID, base))
		return
	}
	alternative := alternatives[index]
	nc.mucNickAltIndex[roomJID] = index + 1
	if nc.mucNickPendingAlt == nil {
		nc.mucNickPendingAlt = map[string]string{}
	}
	nc.mucNickPendingAlt[roomJID] = alternative
	nc.mucNickAltMu.Unlock()

	nc.log.Info().
		Str("room_jid", roomJID).
		Str("nick", alternative).
		Msg("Nickname is in use; retrying MUC join with an alternative")
	nc.joinMUC(roomJID, alternative, nc.mucPasswordFor(roomJID))
}

// confirmMUCNick remembers an alternative nickname once the join with it has
// been confirmed by a self-presence.
func (nc *MyNetworkClient) confirmMUCNick(roomJID, nick string) {
	nc.mucNickAltMu.Lock()
	pending := ""
	if nc.mucNickPendingAlt != nil {
		pending = nc.mucNickPendingAlt[roomJID]
	}
	if pending == "" || pending != nick {
		nc.mucNickAltMu.Unlock()
		return
	}
	delete(nc.mucNickPendingAlt, roomJID)
	if nc.mucNickAltIndex != nil {
		delete(nc.mucNickAltIndex, roomJID)
	}
	nc.mucNickAltMu.Unlock()

	nc.log.Info().Str("room_jid", roomJID).Str("nick", nick).Msg("Joined MUC room with an alternative nickname")
	nc.notifyManagement(fmt.Sprintf("Joined room %s with nickname %q because the preferred one was in use.", roomJID, nick))
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := nc.setMUCNickOverride(ctx, roomJID, nick); err != nil {
			nc.log.Err(err).Str("room_jid", roomJID).Msg("Failed to persist MUC nickname override")
		}
	}()
}
