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
	"unicode"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

const (
	// mucInviteRateLimit is the maximum number of MUC invites per window that
	// the bridge acts on by creating a portal and joining the room.
	mucInviteRateLimit = 10
	// mucInviteRateWindow is the rolling window for the invite rate limit.
	mucInviteRateWindow = time.Hour
	// mucRoomCheckNegativeTTL is how long a negative "is this a MUC room"
	// disco result is cached before the JID is probed again.
	mucRoomCheckNegativeTTL = 10 * time.Minute
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

// mucInvite is a received invitation to a MUC room, from either a mediated
// (XEP-0045) or a direct (XEP-0249) invite.
type mucInvite struct {
	room     string
	inviter  string
	reason   string
	password string
}

// mucRoomCheckEntry caches a "is this JID a MUC room" disco result.
type mucRoomCheckEntry struct {
	isRoom bool
	at     time.Time
}

// isCheogramContactJID reports whether a JID names a Cheogram contact (a phone
// number, a short code or a combined group MMS JID) rather than a room. A
// portal created under such a JID would take over the contact's DM portal key.
func isCheogramContactJID(jid string) bool {
	jid = CanonicalBareJID(jid)
	if jid == "" || !strings.HasSuffix(jid, "@cheogram.com") {
		return false
	}
	local := strings.TrimSuffix(jid, "@cheogram.com")
	if local == "" {
		return false
	}
	if strings.Contains(local, ",") || strings.HasPrefix(local, "+") {
		return true
	}
	return phoneSubscriberLabel(local) != ""
}

// isMUCRoom reports whether a JID is a MUC room: a disco#info answer with a
// conference identity or the MUC feature. When disco fails, well-known MUC
// domain patterns are used as a heuristic. Results are cached (negative
// results expire so a temporarily unreachable room is retried later).
func (nc *MyNetworkClient) isMUCRoom(ctx context.Context, roomJID string) bool {
	roomJID = CanonicalBareJID(roomJID)
	if roomJID == "" {
		return false
	}
	nc.mucRoomCheckMu.Lock()
	entry, cached := nc.mucRoomChecks[roomJID]
	nc.mucRoomCheckMu.Unlock()
	if cached && (entry.isRoom || time.Since(entry.at) < mucRoomCheckNegativeTTL) {
		return entry.isRoom
	}
	confirmed := false
	if nc.isXMPPConnected() {
		if info, err := nc.requestDiscoInfo(ctx, roomJID); err == nil && isMUCService(info) {
			confirmed = true
		}
	}
	if !confirmed {
		confirmed = mucDomainHeuristic(roomJID)
	}
	nc.mucRoomCheckMu.Lock()
	if nc.mucRoomChecks == nil {
		nc.mucRoomChecks = map[string]mucRoomCheckEntry{}
	}
	nc.mucRoomChecks[roomJID] = mucRoomCheckEntry{isRoom: confirmed, at: time.Now()}
	nc.mucRoomCheckMu.Unlock()
	return confirmed
}

// mucDomainHeuristic guesses whether a JID is a room from its domain, matching
// the well-known MUC subdomain conventions.
func mucDomainHeuristic(jid string) bool {
	jid = strings.ToLower(CanonicalBareJID(jid))
	if i := strings.IndexByte(jid, '@'); i >= 0 {
		domain := jid[i+1:]
		for _, prefix := range []string{"conference.", "muc.", "groups."} {
			if strings.HasPrefix(domain, prefix) {
				return true
			}
		}
	}
	return false
}

// actOnMUCInvite turns a received invite into a native Matrix invite: the XMPP
// room is never joined here. The portal is marked left + invite-pending
// (PortalMetadata.MUCLeft / MUCInvitePending) so auto-rejoin, the join
// watchdog and backfill leave it alone until the user accepts the Matrix
// invite (HandleMatrixMembership -> joinMUC).
//
//   - no portal with a Matrix room: create it with the user as an invited
//     member, so bridgev2 sends only the invite and never joins the room;
//   - portal exists, user not joined in Matrix: re-invite via a resync;
//   - portal exists, user joined in Matrix and the XMPP room is left: post a
//     notice pointing at the join-room command.
func (nc *MyNetworkClient) actOnMUCInvite(inv mucInvite) {
	if nc.bridge == nil || nc.login == nil {
		return
	}
	inv.room = CanonicalBareJID(inv.room)
	if inv.room == "" {
		return
	}
	if isCheogramContactJID(inv.room) {
		nc.log.Debug().Str("room_jid", inv.room).Msg("MUC invite: the invited JID is not a room; invite dropped")
		return
	}
	if !nc.allowMUCInvite(inv.room, inv.inviter, inv.reason) {
		return
	}
	if password := strings.TrimSpace(inv.password); password != "" {
		nc.rememberMUCInvitePassword(inv.room, password)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	portal, err := nc.bridge.GetExistingPortalByKey(ctx, networkid.PortalKey{ID: MakePortalID(inv.room)})
	if err != nil {
		nc.log.Warn().Err(err).Str("room_jid", inv.room).Msg("MUC invite: portal lookup failed; invite dropped")
		return
	}
	if portal == nil || portal.MXID == "" {
		if (portal != nil && portal.RoomType == database.RoomTypeDM) || !nc.isMUCRoom(ctx, inv.room) {
			nc.log.Debug().Str("room_jid", inv.room).Msg("MUC invite: the invited JID is not a room; invite dropped")
			return
		}
		nc.log.Info().
			Str("room_jid", inv.room).
			Str("from", inv.inviter).
			Msg("Received MUC invite; creating an invited portal")
		nc.queueInviteResync(ctx, inv, true, inviteText(inv, "Accept the invite to join; the bridge joins the XMPP room then."))
		return
	}
	meta, _ := portal.Metadata.(*PortalMetadata)
	if meta == nil || !meta.MUCLeft {
		nc.log.Debug().Str("room_jid", inv.room).Msg("MUC invite: room is bridged and not left; nothing to do")
		return
	}
	if membership, known := nc.userRoomMembership(ctx, portal.MXID); known && membership != event.MembershipJoin {
		nc.queueInviteResync(ctx, inv, false, inviteText(inv, "Accept the invite to join; the bridge joins the XMPP room then."))
		return
	}
	nc.notifyPortalRoom(ctx, portal.MXID, inviteText(inv, fmt.Sprintf(
		"Send `%s join-room %s` here or in the management room to rejoin.",
		nc.commandPrefix(), inv.room,
	)))
}

// inviteText words the notice posted into the portal room. tail is the
// instruction that closes it.
func inviteText(inv mucInvite, tail string) string {
	who := "Someone"
	if from := strings.TrimSpace(inv.inviter); from != "" {
		who = "`" + from + "`"
	}
	text := who + " invited you to `" + inv.room + "`"
	if reason := sanitizeInviteReason(inv.reason); reason != "" {
		text += ": " + reason
	}
	text += ". " + tail
	if strings.TrimSpace(inv.password) != "" {
		text += " (The room has a password; it was included with the invite.)"
	}
	return text
}

// sanitizeInviteReason drops control characters, flattens whitespace, caps the
// length and escapes Markdown so the reason renders as plain text.
func sanitizeInviteReason(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n >= 300 {
			b.WriteString("...")
			break
		}
		switch {
		case unicode.IsSpace(r):
			r = ' '
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			continue
		}
		if strings.ContainsRune("\\`*_{}[]()#+-.!|<>~&", r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

// inviteMemberList is the member list of an invite resync: just the user,
// invited (never joined).
func inviteMemberList(nc *MyNetworkClient) *bridgev2.ChatMemberList {
	meID := networkid.UserID(nc.login.ID)
	return &bridgev2.ChatMemberList{
		IsFull:           false,
		TotalMemberCount: 1,
		MemberMap: bridgev2.ChatMemberMap{
			meID: {
				EventSender: bridgev2.EventSender{Sender: meID, IsFromMe: true},
				Membership:  event.MembershipInvite,
			},
		},
	}
}

// markInvitePending marks the portal left + invite-pending (idempotent;
// reports whether it changed).
func markInvitePending(meta *PortalMetadata) bool {
	if meta.MUCLeft && meta.MUCInvitePending {
		return false
	}
	meta.MUCLeft, meta.MUCInvitePending = true, true
	return true
}

// queueInviteResync queues the ChatResync that creates (create=true) or
// re-invites into the portal of an invited room. The chat info carries the
// user as an invited member and marks the portal pending in ExtraUpdates,
// which bridgev2 runs before it creates the room; the notice is posted from
// the post-handler, once the room exists.
func (nc *MyNetworkClient) queueInviteResync(ctx context.Context, inv mucInvite, create bool, notice string) {
	stale := false
	resync := &simplevent.ChatResync{
		EventMeta: simplevent.EventMeta{
			Type:         bridgev2.RemoteEventChatResync,
			PortalKey:    networkid.PortalKey{ID: MakePortalID(inv.room)},
			CreatePortal: create,
			Timestamp:    time.Now(),
			PostHandleFunc: func(ctx context.Context, portal *bridgev2.Portal) {
				if stale {
					return
				}
				var mxid id.RoomID
				if portal != nil {
					mxid = portal.MXID
				}
				nc.notifyPortalRoom(ctx, mxid, notice)
			},
		},
		GetChatInfoFunc: func(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
			if portal != nil && portal.MXID != "" {
				if meta, _ := portal.Metadata.(*PortalMetadata); meta == nil || !meta.MUCLeft {
					// Re-checked here, serialized with the accept: never mark a
					// joined room pending again.
					stale = true
					return &bridgev2.ChatInfo{}, nil
				}
			}
			info := &bridgev2.ChatInfo{}
			if create {
				name := groupRoomNameFromJID(ctx, nc.bridge, inv.room, "", nc.contactNameSuffix(), nc.ownInitial(ctx))
				roomType := database.RoomTypeDefault
				info.Name = &name
				info.Type = &roomType
				info.CanBackfill = true
			}
			info.Members = inviteMemberList(nc)
			orig := info.ExtraUpdates
			info.ExtraUpdates = func(ctx context.Context, p *bridgev2.Portal) bool {
				changed := orig != nil && orig(ctx, p)
				meta, _ := p.Metadata.(*PortalMetadata)
				if meta == nil {
					meta = &PortalMetadata{}
					p.Metadata = meta
				}
				return markInvitePending(meta) || changed
			}
			return info, nil
		},
	}
	nc.bridge.QueueRemoteEvent(nc.login, resync)
}

// userRoomMembership is the user's Matrix membership in a portal room as the
// bridge knows it (false when unknown).
func (nc *MyNetworkClient) userRoomMembership(ctx context.Context, mxid id.RoomID) (event.Membership, bool) {
	if nc.bridge == nil || nc.bridge.Matrix == nil || nc.login == nil || nc.login.UserMXID == "" {
		return "", false
	}
	mi, err := nc.bridge.Matrix.GetMemberInfo(ctx, mxid, nc.login.UserMXID)
	if err != nil || mi == nil {
		return "", false
	}
	return mi.Membership, true
}

// notifyPortalRoom posts an m.notice from the bridge bot into a portal room
// (not the management room).
func (nc *MyNetworkClient) notifyPortalRoom(ctx context.Context, mxid id.RoomID, text string) {
	if mxid == "" || nc.bridge == nil || nc.bridge.Bot == nil {
		return
	}
	sendNotice(nc.bridge.Bot, mxid, text)
}

// commandPrefix returns the bridge's command prefix with the default fallback.
func (nc *MyNetworkClient) commandPrefix() string {
	if nc.bridge != nil && nc.bridge.Config != nil && nc.bridge.Config.CommandPrefix != "" {
		return nc.bridge.Config.CommandPrefix
	}
	return "!jmpchat"
}

// recallMUCInvitePassword returns the remembered invite password of a room
// without forgetting it yet.
func (nc *MyNetworkClient) recallMUCInvitePassword(roomJID string) string {
	nc.mucInviteMu.Lock()
	defer nc.mucInviteMu.Unlock()
	if nc.mucInvitePasswords == nil {
		return ""
	}
	return nc.mucInvitePasswords[roomJID]
}

// rememberMUCInvitePassword stores the password of an invite for the eventual
// accept.
func (nc *MyNetworkClient) rememberMUCInvitePassword(roomJID, password string) {
	nc.mucInviteMu.Lock()
	defer nc.mucInviteMu.Unlock()
	if nc.mucInvitePasswords == nil {
		nc.mucInvitePasswords = map[string]string{}
	}
	nc.mucInvitePasswords[roomJID] = password
}

// forgetMUCInvitePassword drops the remembered invite password of a room.
func (nc *MyNetworkClient) forgetMUCInvitePassword(roomJID string) {
	nc.mucInviteMu.Lock()
	defer nc.mucInviteMu.Unlock()
	delete(nc.mucInvitePasswords, roomJID)
}

// isOwnLoginTarget reports whether a membership change targets this client's
// own login.
func (nc *MyNetworkClient) isOwnLoginTarget(target bridgev2.GhostOrUserLogin) bool {
	ul, ok := target.(*bridgev2.UserLogin)
	return ok && ul != nil && nc.login != nil && ul.ID == nc.login.ID
}

// clearMUCInviteState clears the persistent left/invite-pending flags of a
// room's portal (and the in-memory left flag) after the user joined it (or
// explicitly asked to join), so the watchdog and bookmark imports treat it as
// joined again.
func (nc *MyNetworkClient) clearMUCInviteState(ctx context.Context, roomJID string) {
	nc.rejoinMUC(roomJID)
	if nc.bridge == nil || roomJID == "" {
		return
	}
	portal, err := nc.bridge.GetExistingPortalByKey(ctx, networkid.PortalKey{ID: MakePortalID(roomJID)})
	if err != nil {
		nc.log.Warn().Err(err).Str("room_jid", roomJID).Msg("Failed to load portal to clear invite state")
		return
	}
	if portal == nil {
		return
	}
	meta, _ := portal.Metadata.(*PortalMetadata)
	if meta == nil || (!meta.MUCLeft && !meta.MUCInvitePending) {
		return
	}
	meta.MUCLeft, meta.MUCInvitePending = false, false
	if err := portal.Save(ctx); err != nil {
		nc.log.Warn().Err(err).Str("room_jid", roomJID).Msg("Failed to clear invite state on portal")
	}
}

// markPortalMUCLeft persists the "intentionally left" flag on a room's portal
// so the bridge stops treating the room as joined. join-room and accepting an
// invite clear it again through clearMUCInviteState.
func (nc *MyNetworkClient) markPortalMUCLeft(ctx context.Context, roomJID string) {
	if nc.bridge == nil || roomJID == "" {
		return
	}
	portal, err := nc.bridge.GetExistingPortalByKey(ctx, networkid.PortalKey{ID: MakePortalID(roomJID)})
	if err != nil {
		nc.log.Warn().Err(err).Str("room_jid", roomJID).Msg("Failed to load portal to mark it as left")
		return
	}
	if portal == nil {
		return
	}
	meta, _ := portal.Metadata.(*PortalMetadata)
	if meta == nil || meta.MUCLeft {
		return
	}
	meta.MUCLeft, meta.MUCInvitePending = true, false
	if err := portal.Save(ctx); err != nil {
		nc.log.Warn().Err(err).Str("room_jid", roomJID).Msg("Failed to mark portal as left")
	}
}

// joinMUCFromInvite joins a room after an accepted invite: the join carries
// the remembered invite password and the room is bookmarked with autojoin so
// a reconnect rejoins it.
func (nc *MyNetworkClient) joinMUCFromInvite(ctx context.Context, roomJID, password string) {
	if password == "" {
		password = nc.mucPasswordFor(roomJID)
	}
	nc.joinMUC(roomJID, "", password)
	if nc.bookmarkedRooms != nil {
		nc.bookmarkedRooms[roomJID] = true
	}
	name := ""
	if nc.bookmarkedRoomNames != nil {
		name = nc.bookmarkedRoomNames[roomJID]
	}
	if err := nc.publishBookmark2Merged(ctx, BookmarkConference{
		JID:      roomJID,
		Name:     name,
		Autojoin: "true",
		Nick:     nc.mucNickOr(roomJID),
	}, true); err != nil {
		nc.log.Warn().Err(err).Str("room_jid", roomJID).Msg("Failed to publish bookmark after accepting a MUC invite")
	}
}

// HandleMatrixMembership (bridgev2.MembershipHandlingNetworkAPI) makes
// accepting the Matrix invite of an invited MUC portal the explicit "join"
// intent: the left/pending flags are cleared, the XMPP room is joined with the
// invite password and a bookmark is written. Rejecting changes nothing on
// XMPP. Every other change answers ErrMembershipNotSupported, which gives the
// same failed message status as before this interface existed. Own profile
// changes feed the MUC nick sync and are a no-op success.
func (nc *MyNetworkClient) HandleMatrixMembership(ctx context.Context, msg *bridgev2.MatrixMembershipChange) (*bridgev2.MatrixMembershipResult, error) {
	if msg == nil {
		return nil, bridgev2.ErrMembershipNotSupported
	}
	switch msg.Type {
	case bridgev2.AcceptInvite, bridgev2.Join:
		if !nc.isOwnLoginTarget(msg.Target) || msg.Portal == nil {
			return nil, bridgev2.ErrMembershipNotSupported
		}
		meta, _ := msg.Portal.Metadata.(*PortalMetadata)
		if meta == nil || !meta.MUCInvitePending {
			return nil, nil // an ordinary join of a bridged room: nothing to do on XMPP
		}
		room := UnescapeJID(string(msg.Portal.ID))
		if isCheogramContactJID(room) {
			return nil, nil
		}
		password := nc.recallMUCInvitePassword(room)
		nc.clearMUCInviteState(ctx, room)
		nc.joinMUCFromInvite(ctx, room, password)
		nc.forgetMUCInvitePassword(room)
		nc.notifyManagement(fmt.Sprintf("Joined MUC room %s after an accepted invite.", room))
		return nil, nil
	case bridgev2.RejectInvite:
		if nc.isOwnLoginTarget(msg.Target) && msg.Portal != nil {
			nc.forgetMUCInvitePassword(UnescapeJID(string(msg.Portal.ID)))
			return nil, nil
		}
		return nil, bridgev2.ErrMembershipNotSupported
	case bridgev2.ProfileChange:
		nc.handleOwnProfileChange(ctx, msg)
		return nil, nil
	}
	return nil, bridgev2.ErrMembershipNotSupported
}

var _ bridgev2.MembershipHandlingNetworkAPI = (*MyNetworkClient)(nil)
