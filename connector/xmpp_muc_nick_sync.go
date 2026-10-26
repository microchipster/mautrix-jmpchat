// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/matrix"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// Nick sync (network.muc_nick_sync): the user's per-room Matrix display name
// and their MUC nick follow each other.
//
// Matrix -> XMPP: an own profile change in a bridged MUC room (debounced)
// becomes a nick change, unless the new name is the user's global display
// name (which is never sent).
//
// XMPP -> Matrix: own nick changes (confirmed rename, foreign rename by
// another client, an alternative chosen after a conflict, server-assigned
// nick) set the per-room display name through the double puppet, written
// without the fi.mau.double_puppet_source marker so Matrix clients can copy
// it (e.g. Element's /myroomnick) without their change being dropped by the
// bridge. Display names are never logged.

const (
	// nickSyncDebounce is the quiet period after the last room display name
	// change before the nick is changed.
	nickSyncDebounce = 3 * time.Second
	// globalProfileTTL caches the user's global display name.
	globalProfileTTL = 30 * time.Second
	// nickSyncMapCap bounds the own-write echo table and cleanup daily maps.
	nickSyncMapCap = 4096
	// ownWriteTTL is how long our own member event write is remembered for
	// echo detection.
	ownWriteTTL = 2 * time.Minute
	// nickCleanupStagger paces the one-time marker cleanup across portals.
	nickCleanupStagger = 500 * time.Millisecond
	// doublePuppetSourceKey is the marker bridgev2 adds to state events it
	// sends as the user; client copies of such events are dropped by bridgev2,
	// so our own writes must not carry it.
	doublePuppetSourceKey = "fi.mau.double_puppet_source"
)

// nickSyncReq is one debounced Matrix -> XMPP nick request.
type nickSyncReq struct {
	timer       *time.Timer
	display     string // requested room display name (never logged)
	prevDisplay string // PrevContent displayname (revert target; kept from the first change of a burst)
	portalMXID  id.RoomID
}

// ownMemberWrite is one of our own recent member event writes (the display
// name is never logged).
type ownMemberWrite struct {
	display string
	at      time.Time
}

// nickSyncState holds the debounced Matrix -> XMPP nick requests (one per
// portal) plus the own-write echo table and the once-per-session marker
// cleanup bookkeeping.
type nickSyncState struct {
	mu        sync.Mutex
	pending   map[networkid.PortalID]*nickSyncReq
	writes    map[id.RoomID]ownMemberWrite
	cleaned   map[networkid.PortalID]bool
	cleanNext time.Time
}

// schedule (re)arms the debounce timer for a portal; the previous pending
// request of the portal is replaced.
func (s *nickSyncState) schedule(portal networkid.PortalID, req *nickSyncReq, d time.Duration, fire func(*nickSyncReq)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		s.pending = map[networkid.PortalID]*nickSyncReq{}
	}
	if old, ok := s.pending[portal]; ok && old.timer != nil {
		old.timer.Stop()
	}
	s.pending[portal] = req
	req.timer = time.AfterFunc(d, func() {
		s.mu.Lock()
		if cur, ok := s.pending[portal]; !ok || cur != req {
			s.mu.Unlock()
			return
		}
		delete(s.pending, portal)
		s.mu.Unlock()
		fire(req)
	})
}

// recordOwnWrite remembers that we are about to set the room display name to
// display.
func (s *nickSyncState) recordOwnWrite(room id.RoomID, display string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if s.writes == nil || len(s.writes) >= nickSyncMapCap {
		s.writes = map[id.RoomID]ownMemberWrite{}
	}
	for r, w := range s.writes {
		if now.Sub(w.at) > ownWriteTTL {
			delete(s.writes, r)
		}
	}
	s.writes[room] = ownMemberWrite{display: display, at: now}
}

// forgetOwnWrite drops the echo expectation after a failed write.
func (s *nickSyncState) forgetOwnWrite(room id.RoomID, display string) {
	s.mu.Lock()
	if w, ok := s.writes[room]; ok && w.display == display {
		delete(s.writes, room)
	}
	s.mu.Unlock()
}

// consumeOwnWrite reports (and consumes) whether a member event with this
// display name is the echo of a recent write of ours.
func (s *nickSyncState) consumeOwnWrite(room id.RoomID, display string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.writes[room]
	if !ok || w.display != display || time.Since(w.at) > ownWriteTTL {
		return false
	}
	delete(s.writes, room)
	return true
}

// nickSyncPortalOK reports whether portal is a bridged MUC room whose nick
// should follow the Matrix display name.
func (nc *MyNetworkClient) nickSyncPortalOK(portal *database.Portal) bool {
	if portal == nil || portal.MXID == "" || nc.login == nil {
		return false
	}
	if portal.Receiver != "" && portal.Receiver != nc.login.ID {
		return false
	}
	if portal.RoomType == database.RoomTypeDM || isCheogramContactJID(UnescapeJID(string(portal.ID))) {
		return false
	}
	meta, _ := portal.Metadata.(*PortalMetadata)
	if meta != nil {
		if meta.MUCLeft || meta.MUCInvitePending {
			return false
		}
		if meta.IsMUC {
			return true
		}
	}
	// Room type is not known yet (portal was created by a resync): accept
	// well-known MUC domains as a heuristic.
	return mucDomainHeuristic(UnescapeJID(string(portal.ID)))
}

// handleOwnProfileChange is the ProfileChange branch of HandleMatrixMembership
// (portal event loop; memory only, no HTTP): a changed per-room display name
// of the user is scheduled as a nick change.
func (nc *MyNetworkClient) handleOwnProfileChange(ctx context.Context, msg *bridgev2.MatrixMembershipChange) {
	if msg == nil || !nc.simpleConfig().mucNickSyncEnabled() {
		return
	}
	if msg.Portal == nil || msg.Portal.Portal == nil || msg.OrigSender != nil || !nc.isOwnLoginTarget(msg.Target) {
		return
	}
	if nc.login == nil || msg.Event == nil || msg.Event.Sender != nc.login.UserMXID || msg.Event.GetStateKey() != string(nc.login.UserMXID) {
		return
	}
	var newName, oldName string
	if msg.Content != nil {
		newName = msg.Content.Displayname
	}
	if msg.PrevContent != nil {
		oldName = msg.PrevContent.Displayname
	}
	if newName == oldName || strings.TrimSpace(newName) == "" {
		return // avatar-only change, or a cleared name
	}
	if nc.nickSync.consumeOwnWrite(msg.Portal.MXID, newName) {
		return // the echo of our own write (our writes carry no marker)
	}
	if !nc.nickSyncPortalOK(msg.Portal.Portal) {
		return
	}
	portalID := msg.Portal.ID
	req := &nickSyncReq{display: newName, prevDisplay: oldName, portalMXID: msg.Portal.MXID}
	nc.log.Debug().Str("room", string(portalID)).Int("display_len", len(newName)).Msg("Room display name changed, scheduling MUC nick sync")
	nc.nickSync.schedule(portalID, req, nickSyncDebounce, func(r *nickSyncReq) { nc.runNickSyncOut(portalID, r) })
}

// runNickSyncOut applies a debounced room display name as a MUC nick (timer
// goroutine).
func (nc *MyNetworkClient) runNickSyncOut(portalID networkid.PortalID, r *nickSyncReq) {
	if nc.bridge == nil || nc.login == nil || !nc.simpleConfig().mucNickSyncEnabled() || r == nil {
		return
	}
	room := UnescapeJID(string(portalID))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	portal, err := nc.bridge.GetExistingPortalByKey(ctx, networkid.PortalKey{ID: portalID})
	if err != nil || !nc.nickSyncPortalOK(portal.Portal) {
		return
	}
	// A cached global name may be stale: after a global rename the homeserver
	// rewrites the member event of every room with the NEW global name, which
	// must never look like a per-room name (it would rename the user in every
	// MUC). A name that differs from the cache is checked against a fresh
	// profile.
	global, err := nc.globalDisplayName(ctx, false)
	if err == nil && r.display != global {
		global, err = nc.globalDisplayName(ctx, true)
	}
	if err != nil {
		nc.log.Warn().Err(err).Str("room", room).Msg("Cannot read the global display name, room nick not synced")
		return
	}
	if r.display == global {
		nc.log.Debug().Str("room", room).Msg("Room display name equals the global one, ignored")
		return
	}
	nick, reason := sanitizeMUCNick(r.display)
	if reason != "" {
		cur := nc.mucNickOr(room)
		nc.log.Info().Str("room", room).Int("display_len", len(r.display)).Str("reason", reason).Msg("Room display name unusable as MUC nick")
		nc.notifyPortalRoom(ctx, r.portalMXID, fmt.Sprintf(
			"The new room display name cannot be used as a room nickname: %s. The nickname stays %q.",
			reason, cur,
		))
		return
	}
	if !nc.isXMPPConnected() {
		// Not connected: used at the next join.
		if err := nc.setMUCNickOverride(ctx, room, nick); err != nil {
			nc.log.Warn().Err(err).Str("room", room).Msg("Failed to store the MUC nick from the room display name")
		}
		return
	}
	cur := nc.mucNickOr(room)
	if strings.EqualFold(cur, nick) {
		if nc.mucNickOverride(room) != nick {
			if err := nc.setMUCNickOverride(ctx, room, nick); err != nil {
				nc.log.Warn().Err(err).Str("room", room).Msg("Failed to store the MUC nick from the room display name")
			}
		}
		return
	}
	if !nc.mucJoined[room] {
		// Not in the room (join in flight): used at the next join.
		if err := nc.setMUCNickOverride(ctx, room, nick); err != nil {
			nc.log.Warn().Err(err).Str("room", room).Msg("Failed to store the MUC nick from the room display name")
		}
		return
	}
	if err := nc.setMUCNickOverride(ctx, room, nick); err != nil {
		nc.log.Warn().Err(err).Str("room", room).Msg("Failed to store the MUC nick from the room display name")
	}
	nc.sendMUCNickChange(room, nick)
}

// sanitizeMUCNick validates a Matrix display name for use as a MUC nick. The
// reason is a human-readable phrase for the notice ("" when the nick is ok).
func sanitizeMUCNick(raw string) (nick string, reason string) {
	nick = normalizeContactName(raw)
	if nick == "" {
		return "", "it is empty"
	}
	if strings.ContainsAny(nick, " \t") {
		return "", "it contains spaces"
	}
	if len([]rune(nick)) > 64 {
		return "", "it is longer than 64 characters"
	}
	if strings.ContainsAny(nick, "@/,") {
		return "", "it contains characters that are not allowed in a nickname"
	}
	return nick, ""
}

// globalDisplayName returns the user's global Matrix display name (cached
// globalProfileTTL; fresh skips the cache).
func (nc *MyNetworkClient) globalDisplayName(ctx context.Context, fresh bool) (string, error) {
	nc.globalNameMu.Lock()
	if !fresh && !nc.globalNameAt.IsZero() && time.Since(nc.globalNameAt) < globalProfileTTL {
		name := nc.globalName
		nc.globalNameMu.Unlock()
		return name, nil
	}
	nc.globalNameMu.Unlock()
	mc, ok := nc.bridge.Matrix.(*matrix.Connector)
	if !ok || mc == nil || mc.AS == nil || nc.login == nil {
		return "", fmt.Errorf("unsupported Matrix connector")
	}
	prof, err := mc.AS.BotClient().GetProfile(ctx, nc.login.UserMXID)
	if err != nil {
		return "", err
	}
	name := prof.DisplayName
	nc.globalNameMu.Lock()
	nc.globalName, nc.globalNameAt = name, time.Now()
	nc.globalNameMu.Unlock()
	return name, nil
}

// ownDoublePuppet returns the double-puppet Matrix API of this login.
func (nc *MyNetworkClient) ownDoublePuppet(ctx context.Context) (bridgev2.MatrixAPI, bool) {
	if nc.login == nil || nc.login.User == nil {
		return nil, false
	}
	dp := nc.login.User.DoublePuppet(ctx)
	if dp == nil {
		return nil, false
	}
	return dp, true
}

// marshalToMap converts a member event content to a raw map for editing.
func marshalToMap(raw map[string]any) map[string]any {
	out := make(map[string]any, len(raw))
	for k, v := range raw {
		out[k] = v
	}
	return out
}

// readOwnMember reads the user's member event of a room: the raw content, the
// membership and the display name.
func (nc *MyNetworkClient) readOwnMember(ctx context.Context, room id.RoomID) (raw map[string]any, membership, display string, err error) {
	if _, ok := nc.ownDoublePuppet(ctx); !ok {
		return nil, "", "", fmt.Errorf("no double puppet")
	}
	mconn, ok := nc.bridge.Matrix.(bridgev2.MatrixConnectorWithArbitraryRoomState)
	if !ok {
		return nil, "", "", fmt.Errorf("Matrix connector does not support arbitrary room state")
	}
	evt, err := mconn.GetStateEvent(ctx, room, event.StateMember, string(nc.login.UserMXID))
	if err != nil {
		return nil, "", "", err
	}
	if evt == nil {
		return nil, "", "", fmt.Errorf("member event not found")
	}
	raw = marshalToMap(evt.Content.Raw)
	if membership = strings.TrimSpace(fmt.Sprint(raw["membership"])); membership == "" {
		membership = string(event.MembershipJoin)
	}
	if display, _ = raw["displayname"].(string); display == "" {
		if content, ok := evt.Content.Parsed.(*event.MemberEventContent); ok && content != nil {
			display = content.Displayname
		}
	}
	return raw, membership, display, nil
}

// cleanMemberContent strips the double-puppet marker (that bridgev2 adds to
// state events it sends as the user) from a member event content.
func cleanMemberContent(raw map[string]any) map[string]any {
	out := marshalToMap(raw)
	delete(out, doublePuppetSourceKey)
	return out
}

// sendOwnMember sends a member event as the user's double puppet. The
// appservice intent path is used directly so that bridgev2 does not add its
// double-puppet marker (client copies of marked events are dropped).
func (nc *MyNetworkClient) sendOwnMember(ctx context.Context, dp bridgev2.MatrixAPI, room id.RoomID, content map[string]any) error {
	if dp == nil || nc.login == nil {
		return fmt.Errorf("no double puppet")
	}
	if as, ok := dp.(*matrix.ASIntent); ok && as != nil && as.Matrix != nil && as.Matrix.Client != nil {
		_, err := as.Matrix.Client.SendStateEvent(ctx, room, event.StateMember, string(nc.login.UserMXID), content)
		return err
	}
	_, err := dp.SendState(ctx, room, event.StateMember, string(nc.login.UserMXID), &event.Content{Raw: content}, time.Now())
	return err
}

// writeOwnDisplayname sets the displayname of the user's member event through
// the double puppet, keeping every other key (avatar_url, unknown keys) but no
// marker. The write is recorded first, so its echo is recognized by
// handleOwnProfileChange (no loop).
func (nc *MyNetworkClient) writeOwnDisplayname(ctx context.Context, dp bridgev2.MatrixAPI, room id.RoomID, raw map[string]any, name string) error {
	newRaw := cleanMemberContent(raw)
	newRaw["displayname"] = name
	nc.nickSync.recordOwnWrite(room, name)
	err := nc.sendOwnMember(ctx, dp, room, newRaw)
	if err != nil {
		nc.nickSync.forgetOwnWrite(room, name)
	}
	return err
}

// syncOwnNickToMatrix sets the user's per-room Matrix display name to nick
// (double puppet only; never on the Serve goroutine).
func (nc *MyNetworkClient) syncOwnNickToMatrix(ctx context.Context, roomBare, nick string) {
	if nc.bridge == nil || nc.login == nil || !nc.simpleConfig().mucNickSyncEnabled() || strings.TrimSpace(nick) == "" {
		return
	}
	portal, err := nc.bridge.GetExistingPortalByKey(ctx, networkid.PortalKey{ID: MakePortalID(roomBare)})
	if err != nil || !nc.nickSyncPortalOK(portal.Portal) {
		return
	}
	dp, ok := nc.ownDoublePuppet(ctx)
	if !ok {
		nc.log.Debug().Str("room", roomBare).Msg("No double puppet, MUC nick not synced to the room display name")
		return
	}
	raw, membership, display, err := nc.readOwnMember(ctx, portal.MXID)
	if err != nil {
		nc.log.Debug().Err(err).Str("room", roomBare).Msg("Cannot read the member event, MUC nick not synced")
		return
	}
	if membership != string(event.MembershipJoin) || display == nick {
		return // setting a display name of a non-member would join the user
	}
	if err := nc.writeOwnDisplayname(ctx, dp, portal.MXID, raw, nick); err != nil {
		nc.log.Warn().Err(err).Str("room", roomBare).Msg("Failed to set the room display name from the MUC nick")
		return
	}
	nc.log.Info().Str("room", roomBare).Int("nick_len", len(nick)).Msg("Room display name set from the MUC nick")
}

// syncOwnNickToMatrixAsync is the goroutine entry used from the Serve
// goroutine.
func (nc *MyNetworkClient) syncOwnNickToMatrixAsync(roomBare, nick string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	nc.syncOwnNickToMatrix(ctx, roomBare, nick)
}

// nickSyncCleanupAsync (goroutine; started when our join of a room is
// confirmed) strips the double-puppet marker from the user's existing member
// event once per portal per session, so Element's /myroomnick copy of it can
// no longer be dropped by bridgev2. Displayname and avatar are never changed.
func (nc *MyNetworkClient) nickSyncCleanupAsync(roomBare string) {
	if nc.bridge == nil || nc.login == nil || !nc.simpleConfig().mucNickSyncEnabled() {
		return
	}
	portalID := networkid.PortalID(roomBare)
	s := &nc.nickSync
	s.mu.Lock()
	if s.cleaned[portalID] {
		s.mu.Unlock()
		return
	}
	if s.cleaned == nil {
		s.cleaned = map[networkid.PortalID]bool{}
	}
	s.cleaned[portalID] = true
	slot := time.Now()
	if s.cleanNext.After(slot) {
		slot = s.cleanNext
	}
	s.cleanNext = slot.Add(nickCleanupStagger)
	s.mu.Unlock()
	if d := time.Until(slot); d > 0 {
		time.Sleep(d)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if !nc.simpleConfig().mucNickSyncEnabled() {
		return
	}
	portal, err := nc.bridge.GetExistingPortalByKey(ctx, networkid.PortalKey{ID: portalID})
	if err != nil || !nc.nickSyncPortalOK(portal.Portal) {
		return
	}
	dp, ok := nc.ownDoublePuppet(ctx)
	if !ok {
		return
	}
	raw, membership, display, err := nc.readOwnMember(ctx, portal.MXID)
	if err != nil || membership != string(event.MembershipJoin) {
		return
	}
	if _, marked := raw[doublePuppetSourceKey]; !marked {
		return
	}
	clean := cleanMemberContent(raw)
	s.recordOwnWrite(portal.MXID, display)
	if err := nc.sendOwnMember(ctx, dp, portal.MXID, clean); err != nil {
		s.forgetOwnWrite(portal.MXID, display)
		nc.log.Warn().Err(err).Str("room", roomBare).Msg("Failed to strip the double puppet marker from the member event")
		return
	}
	nc.log.Debug().Str("room", roomBare).Msg("Stripped the double puppet marker from the member event")
}

// handleOwnMUCNickPresence is called on a confirmed self-presence. When our
// occupant nick changed without us asking (another client sharing the login,
// a server-assigned nick, a rename through another bridge instance), the new
// nick becomes the room's explicit nickname so a reconnect does not rename
// back, and it is synced to the Matrix room display name.
func (nc *MyNetworkClient) handleOwnMUCNickPresence(roomBare, nick string) {
	nick = strings.TrimSpace(nick)
	if nick == "" {
		return
	}
	prev := ""
	if nc.mucNick != nil {
		prev = strings.TrimSpace(nc.mucNick[roomBare])
	}
	if strings.EqualFold(prev, nick) {
		return
	}
	if nc.mucNick == nil {
		nc.mucNick = map[string]string{}
	}
	nc.mucNick[roomBare] = nick
	if prev == "" || !nc.simpleConfig().mucNickSyncEnabled() {
		return
	}
	nc.log.Info().Str("room_jid", roomBare).Str("nick", nick).Msg("Occupant nickname changed on the XMPP side")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := nc.setMUCNickOverride(ctx, roomBare, nick); err != nil {
			nc.log.Warn().Err(err).Str("room_jid", roomBare).Msg("Failed to persist the changed MUC nickname")
		}
	}()
	go nc.syncOwnNickToMatrixAsync(roomBare, nick)
}
