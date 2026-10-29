//go:build sipcalls

// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"fmt"
	"html"
	"strings"

	"github.com/williamkray/mrtc-sip-bridge/pkg/core"
	"github.com/williamkray/mrtc-sip-bridge/pkg/hostkit"
	"github.com/williamkray/mrtc-sip-bridge/pkg/phone"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// callHost implements core.CallHost: it maps SIP logins to bridge logins and remote numbers to
// Cheogram portal rooms, creating the room when a call needs one.
type callHost struct{ x *callsExt }

var _ core.CallHost = (*callHost)(nil)

func (x *callsExt) callHost() core.CallHost { return &callHost{x: x} }

// login resolves a LoginRef to the bridge login and its XMPP client.
func (h *callHost) login(ref core.LoginRef) (*bridgev2.UserLogin, *MyNetworkClient, error) {
	if h.x.br == nil {
		return nil, nil, core.ErrUnknownLogin
	}
	ul := h.x.br.GetCachedUserLoginByID(networkid.UserLoginID(ref))
	if ul == nil {
		return nil, nil, core.ErrUnknownLogin
	}
	client, _ := ul.Client.(*MyNetworkClient)
	return ul, client, nil
}

// callRoomDecision says where a call for a portal should ring.
type callRoomDecision int

const (
	// callRoomUsePortal rings in the portal room itself (unencrypted portal, no linked call room).
	callRoomUsePortal callRoomDecision = iota
	// callRoomUseCallRoom rings in the linked unencrypted call room.
	callRoomUseCallRoom
	// callRoomRefuse cannot ring: the portal is encrypted and has no linked call room.
	callRoomRefuse
)

// decideCallRoom picks the ringing target for a portal. In portal mode (the default) calls ring in
// the phone number's own chat room, encrypted or not. The legacy call room is used when the mode is
// call-room, or when the portal is encrypted and call encryption is turned off. A linked call room
// wins over refusing.
func decideCallRoom(mode CallRoomMode, portalEncrypted bool, callRoom id.RoomID, e2ee bool) callRoomDecision {
	if mode == CallRoomModeCallRoom || (portalEncrypted && !e2ee) {
		if callRoom != "" {
			return callRoomUseCallRoom
		}
		if portalEncrypted {
			return callRoomRefuse
		}
	}
	return callRoomUsePortal
}

// callRoomName builds the display name of a dedicated call room.
func callRoomName(displayName, number string) string {
	displayName = strings.TrimSpace(displayName)
	if displayName == "" || displayName == number {
		return "📞 " + number
	}
	return fmt.Sprintf("📞 %s (%s)", displayName, number)
}

// callRoomTopic is the fixed topic of dedicated call rooms.
func callRoomTopic(number string) string {
	return fmt.Sprintf("Dedicated unencrypted room for phone calls with %s. Chat stays in the encrypted portal room; calls ring here.", number)
}

// portalCallRoomID returns the linked call room of a portal, if any.
func portalCallRoomID(portal *bridgev2.Portal) id.RoomID {
	if portal == nil {
		return ""
	}
	meta, ok := portal.Metadata.(*PortalMetadata)
	if !ok || meta == nil {
		return ""
	}
	return meta.CallRoomMXID
}

// ResolveCallRoom returns (creating if needed) the room of e164 for the login: the linked call
// room when one exists, otherwise the portal room (which must be unencrypted).
func (h *callHost) ResolveCallRoom(ctx context.Context, login core.LoginRef, e164 string) (*core.CallRoom, error) {
	ul, client, err := h.login(login)
	if err != nil {
		return nil, err
	}
	if !phone.IsE164(e164) {
		return nil, fmt.Errorf("%s is not an E.164 phone number", e164)
	}
	jid := e164 + "@cheogram.com"
	portalKey := networkid.PortalKey{ID: MakePortalID(jid)}
	if h.x.br.Config != nil && h.x.br.Config.SplitPortals {
		portalKey.Receiver = ul.ID
	}
	portal, err := h.x.br.GetPortalByKey(ctx, portalKey)
	if err != nil {
		return nil, fmt.Errorf("load call portal: %w", err)
	}

	callRoom := portalCallRoomID(portal)
	portalEncrypted := false
	if portal.MXID != "" {
		portalEncrypted = hostkit.RoomEncrypted(ctx, h.x.br, portal.MXID)
	}
	switch decideCallRoom(h.x.cfg.CallRoomMode(), portalEncrypted, callRoom, h.x.cfg.E2EEEnabled()) {
	case callRoomUseCallRoom:
		if hostkit.RoomEncrypted(ctx, h.x.br, callRoom) {
			err := fmt.Errorf("the call room for %s is encrypted; recreate it with `%s call-room --remove %s` and `%s call-room %s`", e164, h.x.commandPrefix(), e164, h.x.commandPrefix(), e164)
			h.x.notifyManagement(ul.ID, fmt.Sprintf("Call from %s could not ring: the linked call room is encrypted.", e164))
			return nil, err
		}
		h.ensureCallPowerLevels(ctx, callRoom)
		h.x.setCallRoomIndex(callRoom, portal.PortalKey)
		return h.roomForRoom(ctx, ul, e164, callRoom, false)
	case callRoomRefuse:
		if !h.x.cfg.IsAutoCreateCallRoom() {
			h.x.notifyManagement(ul.ID, fmt.Sprintf("Call from %s could not ring: the portal room is encrypted. Run `%s call-room %s` to create an unencrypted call room.", e164, h.x.commandPrefix(), e164))
			return nil, fmt.Errorf("the portal room for %s is encrypted; run `%s call-room %s` to create an unencrypted call room", e164, h.x.commandPrefix(), e164)
		}
		roomID, err := h.x.createCallRoomForPortal(ctx, ul, portal, e164)
		if err != nil {
			h.x.notifyManagement(ul.ID, fmt.Sprintf("Call from %s could not ring: creating a call room failed.", e164))
			return nil, err
		}
		h.x.notifyManagement(ul.ID, fmt.Sprintf("Created a call room for %s: %s", e164, roomID.URI().MatrixToURL()))
		return h.roomForRoom(ctx, ul, e164, roomID, true)
	default:
		created := false
		if portal.MXID == "" {
			if client == nil {
				return nil, core.ErrUnknownLogin
			}
			info, err := client.GetChatInfo(ctx, portal)
			if err != nil {
				return nil, fmt.Errorf("build call room info: %w", err)
			}
			if err := portal.CreateMatrixRoom(ctx, ul, info); err != nil {
				return nil, fmt.Errorf("create call room: %w", err)
			}
			created = true
		}
		h.ensureCallPowerLevels(ctx, portal.MXID)
		return h.roomForRoom(ctx, ul, e164, portal.MXID, created)
	}
}

// ensureCallPowerLevels merges the call power-level overrides into a room (best effort).
func (h *callHost) ensureCallPowerLevels(ctx context.Context, roomID id.RoomID) {
	if roomID == "" {
		return
	}
	if changed, err := hostkit.EnsureCallPowerLevels(ctx, h.x.br, roomID); err != nil {
		h.x.log.Warn().Err(err).Str("room_id", string(roomID)).Msg("Failed to ensure call power levels")
	} else if changed {
		h.x.log.Debug().Str("room_id", string(roomID)).Msg("Updated call power levels")
	}
}

// roomForRoom builds the core.CallRoom for an existing Matrix room (portal room or call room).
func (h *callHost) roomForRoom(ctx context.Context, ul *bridgev2.UserLogin, e164 string, roomID id.RoomID, created bool) (*core.CallRoom, error) {
	ghost, err := h.x.br.GetGhostByID(ctx, MakeUserID(e164+"@cheogram.com"))
	if err != nil {
		return nil, fmt.Errorf("load call ghost: %w", err)
	}
	if roomID != "" {
		if err := ghost.Intent.EnsureJoined(ctx, roomID); err != nil {
			h.x.log.Warn().Err(err).Str("room_id", string(roomID)).Msg("Failed to join the call room")
		}
	}
	gi, err := hostkit.NewGhostIntent(ghost)
	if err != nil {
		return nil, err
	}
	encrypted := hostkit.RoomEncrypted(ctx, h.x.br, roomID)
	var frameCrypto core.FrameCrypto
	if encrypted && h.x.cfg.E2EEEnabled() && h.x.keys != nil {
		// Sign the call keys as this ghost. Its Matrix user ID is needed here, not the network
		// user ID: the latter is not a valid Matrix user and the device creation as it fails
		// with "Application service cannot masquerade as this user".
		sess, err := h.x.keys.EnsureSession(ctx, roomID, gi.UserID())
		if err != nil {
			return nil, fmt.Errorf("set up call encryption keys for %s: %w", roomID, err)
		}
		frameCrypto = sess
	}
	return &core.CallRoom{
		RoomID:     roomID,
		RemoteE164: e164,
		Ghost:      gi,
		Encrypted:  encrypted,
		Crypto:     frameCrypto,
		Created:    created,
	}, nil
}

// LookupCallRoom finds the call room of an existing Matrix room (portal room or linked call
// room), or returns ErrNotCallRoom.
func (h *callHost) LookupCallRoom(ctx context.Context, login core.LoginRef, roomID id.RoomID) (*core.CallRoom, error) {
	ul, _, err := h.login(login)
	if err != nil {
		return nil, err
	}
	if roomID == "" {
		return nil, core.ErrNotCallRoom
	}
	if portal, err := h.x.br.GetPortalByMXID(ctx, roomID); err == nil && portal != nil {
		if e164, ok := h.x.isCallPortal(portal, ul.ID); ok {
			return h.roomForRoom(ctx, ul, e164, roomID, false)
		}
	}
	portal := h.x.portalForCallRoom(ctx, ul, roomID)
	if portal == nil {
		return nil, core.ErrNotCallRoom
	}
	e164, ok := h.x.isCallPortal(portal, ul.ID)
	if !ok {
		return nil, core.ErrNotCallRoom
	}
	return h.roomForRoom(ctx, ul, e164, roomID, false)
}

// portalForCallRoom finds the portal whose linked call room is roomID (cache first, then scan).
func (x *callsExt) portalForCallRoom(ctx context.Context, ul *bridgev2.UserLogin, roomID id.RoomID) *bridgev2.Portal {
	if ul == nil || x.br == nil {
		return nil
	}
	if key, ok := x.cachedCallRoomPortal(roomID); ok {
		if portal, err := x.br.GetExistingPortalByKey(ctx, key); err == nil && portal != nil {
			return portal
		}
	}
	rows, err := x.br.DB.UserPortal.GetAllForLogin(ctx, ul.UserLogin)
	if err != nil {
		x.log.Warn().Err(err).Msg("Failed to scan portals for a call room lookup")
		return nil
	}
	for _, row := range rows {
		portal, err := x.br.GetExistingPortalByKey(ctx, row.Portal)
		if err != nil || portal == nil {
			continue
		}
		if portalCallRoomID(portal) == roomID {
			x.setCallRoomIndex(roomID, portal.PortalKey)
			return portal
		}
	}
	return nil
}

// createCallRoomForPortal creates a dedicated unencrypted room for call traffic and links it to
// the portal. The owner is invited; the number's ghost joins immediately.
func (x *callsExt) createCallRoomForPortal(ctx context.Context, ul *bridgev2.UserLogin, portal *bridgev2.Portal, e164 string) (id.RoomID, error) {
	if ul == nil || portal == nil || x.br == nil {
		return "", fmt.Errorf("no portal to link the call room to")
	}
	jid := e164 + "@cheogram.com"
	display := contactDisplayName(ctx, x.br, jid)
	ghost, err := x.br.GetGhostByID(ctx, MakeUserID(jid))
	if err != nil {
		return "", fmt.Errorf("load call ghost: %w", err)
	}
	invite := []id.UserID{ul.UserMXID}
	if ghost != nil {
		invite = append(invite, ghost.Intent.GetMXID())
	}
	req := &mautrix.ReqCreateRoom{
		Visibility: "private",
		Preset:     "private_chat",
		Name:       callRoomName(display, e164),
		Topic:      callRoomTopic(e164),
		Invite:     invite,
		PowerLevelOverride: &event.PowerLevelsEventContent{
			// Nobody may turn encryption on: Element Call would use SFrame E2EE and the
			// bridge's audio would decode to silence (mrtc-sip-bridge ADR-002).
			Events: map[string]int{event.StateEncryption.Type: 100},
			Users: map[id.UserID]int{
				x.br.Bot.GetMXID(): 9001,
				ul.UserMXID:        50,
			},
		},
	}
	roomID, err := x.br.Bot.CreateRoom(ctx, req)
	if err != nil {
		return "", fmt.Errorf("create call room: %w", err)
	}
	if ghost != nil {
		if err := ghost.Intent.EnsureJoined(ctx, roomID); err != nil {
			x.log.Warn().Err(err).Str("room_id", string(roomID)).Msg("Failed to join the call ghost to the new call room")
		}
	}
	if changed, err := hostkit.EnsureCallPowerLevels(ctx, x.br, roomID); err != nil {
		x.log.Warn().Err(err).Str("room_id", string(roomID)).Msg("Failed to ensure call power levels in the new call room")
	} else if changed {
		x.log.Debug().Str("room_id", string(roomID)).Msg("Updated call power levels in the new call room")
	}
	meta, _ := portal.Metadata.(*PortalMetadata)
	if meta == nil {
		meta = &PortalMetadata{}
	}
	meta.CallRoomMXID = roomID
	portal.Metadata = meta
	if err := portal.Save(ctx); err != nil {
		return roomID, fmt.Errorf("save the call room link: %w", err)
	}
	x.setCallRoomIndex(roomID, portal.PortalKey)
	x.log.Info().Str("call_room", string(roomID)).Str("portal_id", string(portal.ID)).Msg("Linked a call room to a portal")
	return roomID, nil
}

// SendCallNotice posts a plain text notice in the call room as the number's ghost.
// When the call room is a linked room for an encrypted portal, the notice is
// also mirrored into the portal room so the call is visible where the chat lives.
func (h *callHost) SendCallNotice(ctx context.Context, room *core.CallRoom, text string) error {
	if room == nil || room.Ghost == nil {
		return fmt.Errorf("no call room for the notice")
	}
	_, err := room.Ghost.SendMessage(ctx, room.RoomID, event.EventMessage, map[string]any{
		"msgtype": string(event.MsgNotice),
		"body":    text,
	})
	h.mirrorCallNotice(ctx, room, text)
	return err
}

// mirrorCallNotice posts the same notice (with a link to the call room) into
// the linked portal room when the call room is not the portal room itself.
// It is best-effort: failures are logged and never affect the call.
func (h *callHost) mirrorCallNotice(ctx context.Context, room *core.CallRoom, text string) {
	if h == nil || h.x == nil || h.x.br == nil {
		return
	}
	key, ok := h.x.cachedCallRoomPortal(room.RoomID)
	if !ok {
		return
	}
	portal, err := h.x.br.GetExistingPortalByKey(ctx, key)
	if err != nil || portal == nil || portal.MXID == "" || portal.MXID == room.RoomID {
		return
	}
	url := room.RoomID.URI().MatrixToURL()
	_, err = h.x.br.Bot.SendMessage(ctx, portal.MXID, event.EventMessage, &event.Content{
		Parsed: &event.MessageEventContent{
			MsgType:       event.MsgNotice,
			Body:          text + "\n" + url,
			Format:        event.FormatHTML,
			FormattedBody: html.EscapeString(text) + "<br/><a href=\"" + url + "\">Open the call room</a>",
		},
	}, nil)
	if err != nil {
		h.x.log.Debug().Err(err).Str("portal_mxid", string(portal.MXID)).Msg("Failed to mirror the call notice into the portal room")
	}
}

// OwnerMXID returns the Matrix user that owns the login ("" when unknown).
func (h *callHost) OwnerMXID(login core.LoginRef) id.UserID {
	if h.x.br == nil {
		return ""
	}
	ul := h.x.br.GetCachedUserLoginByID(networkid.UserLoginID(login))
	if ul == nil {
		return ""
	}
	return ul.UserMXID
}

// MarkReadAsOwner marks the event as read by the Matrix user (double puppet).
func (h *callHost) MarkReadAsOwner(ctx context.Context, login core.LoginRef, roomID id.RoomID, eventID id.EventID) error {
	ul, _, err := h.login(login)
	if err != nil {
		return err
	}
	return hostkit.MarkReadAsOwner(ctx, ul, roomID, eventID)
}

// isCallPortal reports whether portal is a 1:1 Cheogram room of loginID and returns its E.164.
func (x *callsExt) isCallPortal(portal *bridgev2.Portal, loginID networkid.UserLoginID) (string, bool) {
	if portal == nil {
		return "", false
	}
	if portal.Receiver != "" && portal.Receiver != loginID {
		return "", false
	}
	if portalUsesGroupChat(portal) {
		return "", false
	}
	return e164FromCheogramJID(UnescapeJID(string(portal.ID)))
}
