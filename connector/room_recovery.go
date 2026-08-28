// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"fmt"

	"go.mau.fi/util/ptr"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// ensurePortalRoomHealthy verifies that a portal's Matrix room is live, accessible by the bot,
// not tombstoned, and that the Matrix user and remote participants are present. It follows
// confirmed tombstone replacements, but leaves uncertain or temporarily inaccessible rooms alone.
func (nc *MyNetworkClient) ensurePortalRoomHealthy(ctx context.Context, portal *bridgev2.Portal) error {
	if portal == nil || portal.MXID == "" {
		return nil
	}
	if nc.bridge == nil || nc.bridge.Bot == nil || nc.login == nil {
		return nil
	}
	roomID := portal.MXID

	// 1. Check if bot can access the room and is joined.
	err := nc.bridge.Bot.EnsureJoined(ctx, roomID)
	if err != nil {
		nc.log.Warn().
			Err(err).
			Str("room_id", string(roomID)).
			Str("portal_id", string(portal.ID)).
			Msg("Portal Matrix room is inaccessible to bot; refusing automatic room replacement")
		return fmt.Errorf("portal room %s is inaccessible: %w", roomID, err)
	}

	// 2. Check if the room has a tombstone.
	if mc, ok := nc.bridge.Matrix.(bridgev2.MatrixConnectorWithArbitraryRoomState); ok {
		tombstoneEvt, err := mc.GetStateEvent(ctx, roomID, event.StateTombstone, "")
		if err == nil && tombstoneEvt != nil && tombstoneEvt.Content.AsTombstone() != nil {
			tombstoneContent := tombstoneEvt.Content.AsTombstone()
			if tombstoneContent.ReplacementRoom != "" && tombstoneContent.ReplacementRoom != roomID {
				nc.log.Info().
					Str("old_room", string(roomID)).
					Str("replacement_room", string(tombstoneContent.ReplacementRoom)).
					Msg("Portal room has tombstone; checking replacement room")
				if joinErr := nc.bridge.Bot.EnsureJoined(ctx, tombstoneContent.ReplacementRoom); joinErr == nil {
					info, _ := nc.GetChatInfo(ctx, portal)
					_ = portal.UpdateMatrixRoomID(ctx, tombstoneContent.ReplacementRoom, bridgev2.UpdateMatrixRoomIDParams{
						FailIfMXIDSet:      false,
						OverwriteOldPortal: true,
						TombstoneOldRoom:   false,
						DeleteOldRoom:      false,
						ChatInfo:           info,
						ChatInfoSource:     nc.login,
					})
					nc.login.MarkInPortal(ctx, portal)
					roomID = tombstoneContent.ReplacementRoom
				} else {
					nc.log.Warn().
						Err(joinErr).
						Str("replacement_room", string(tombstoneContent.ReplacementRoom)).
						Msg("Tombstone replacement room inaccessible; refusing automatic room replacement")
					return fmt.Errorf("tombstone replacement room %s is inaccessible: %w", tombstoneContent.ReplacementRoom, joinErr)
				}
			}
		}
	}

	// 3. Ensure user is in room (re-invite if left or never joined).
	if nc.login.UserMXID != "" {
		inviteErr := nc.bridge.Bot.EnsureInvited(ctx, roomID, nc.login.UserMXID)
		if inviteErr != nil {
			nc.log.Warn().
				Err(inviteErr).
				Str("room_id", string(roomID)).
				Str("user_mxid", string(nc.login.UserMXID)).
				Msg("Failed to ensure user invited to portal room; refusing automatic room replacement")
			return fmt.Errorf("failed to ensure user invited to portal room: %w", inviteErr)
		}
	}

	// 4. Ensure every remote participant has its own ghost in group rooms.
	for _, participant := range portalParticipantJIDs(portal) {
		ghost, err := nc.bridge.GetGhostByID(ctx, MakeUserID(participant))
		if err != nil {
			return fmt.Errorf("failed to load ghost for %s: %w", participant, err)
		}
		if ghost == nil {
			continue
		}
		if err := ghost.Intent.EnsureJoined(ctx, roomID); err != nil {
			return fmt.Errorf("failed to ensure ghost %s joined portal room: %w", participant, err)
		}
	}

	// 5. Ensure room is in user's Space.
	if nc.login.SpaceRoom != "" {
		_ = addRoomToSpace(ctx, nc.bridge, nc.login.SpaceRoom, roomID, false)
	}

	nc.login.MarkInPortal(ctx, portal)
	return nil
}

// recreatePortalRoom creates a brand new Matrix room for a portal, ensures memberships and power levels,
// and rebinds the portal to the new room.
func (nc *MyNetworkClient) recreatePortalRoom(ctx context.Context, portal *bridgev2.Portal) error {
	if portal == nil {
		return fmt.Errorf("portal is nil")
	}
	if nc.bridge == nil || nc.bridge.Bot == nil || nc.login == nil {
		return fmt.Errorf("bridge or login is nil")
	}

	targetJID := UnescapeJID(string(portal.ID))
	info, err := nc.GetChatInfo(ctx, portal)
	if err != nil || info == nil {
		name := dmRoomName(ctx, nc.bridge, targetJID, "", nc.contactNameSuffix())
		info = &bridgev2.ChatInfo{
			Name: &name,
			Type: ptr.Ptr(database.RoomTypeDM),
		}
	}

	roomName := portal.Name
	if roomName == "" && info.Name != nil {
		roomName = *info.Name
	}
	if roomName == "" {
		roomName = targetJID
	}

	inviteList := []id.UserID{nc.login.UserMXID}
	for _, participant := range portalParticipantJIDs(portal) {
		ghost, err := nc.bridge.GetGhostByID(ctx, MakeUserID(participant))
		if err != nil {
			return fmt.Errorf("failed to load ghost for %s: %w", participant, err)
		}
		if ghost != nil {
			inviteList = append(inviteList, ghost.Intent.GetMXID())
		}
	}

	isDM := portal.RoomType == database.RoomTypeDM
	if portalUsesGroupChat(portal) {
		isDM = false
	}

	req := &mautrix.ReqCreateRoom{
		Visibility: "private",
		Preset:     "private_chat",
		IsDirect:   isDM,
		Name:       roomName,
		Invite:     inviteList,
		PowerLevelOverride: &event.PowerLevelsEventContent{
			Events: map[string]int{
				event.StateTombstone.Type:  100,
				event.StateEncryption.Type: 100,
			},
			Users: map[id.UserID]int{
				nc.bridge.Bot.GetMXID(): 9001,
				nc.login.UserMXID:       50,
			},
		},
	}

	newRoomID, err := nc.bridge.Bot.CreateRoom(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to create replacement Matrix room: %w", err)
	}

	oldMXID := portal.MXID

	for _, participant := range portalParticipantJIDs(portal) {
		ghost, err := nc.bridge.GetGhostByID(ctx, MakeUserID(participant))
		if err != nil {
			return fmt.Errorf("failed to load ghost for %s after creating room: %w", participant, err)
		}
		if ghost != nil {
			if err := ghost.Intent.EnsureJoined(ctx, newRoomID); err != nil {
				return fmt.Errorf("failed to join ghost %s to replacement room: %w", participant, err)
			}
		}
	}

	err = portal.UpdateMatrixRoomID(ctx, newRoomID, bridgev2.UpdateMatrixRoomIDParams{
		FailIfMXIDSet:      false,
		OverwriteOldPortal: true,
		TombstoneOldRoom:   false,
		DeleteOldRoom:      false,
		ChatInfo:           info,
		ChatInfoSource:     nc.login,
	})
	if err != nil {
		return fmt.Errorf("failed to rebind portal to recreated room %s: %w", newRoomID, err)
	}

	if oldMXID != "" && oldMXID != newRoomID && nc.login.SpaceRoom != "" {
		_ = removeRoomFromSpace(ctx, nc.bridge.Bot, nc.login.SpaceRoom, oldMXID)
	}
	if nc.login.SpaceRoom != "" {
		_ = addRoomToSpace(ctx, nc.bridge, nc.login.SpaceRoom, newRoomID, false)
	}

	nc.login.MarkInPortal(ctx, portal)
	nc.log.Info().
		Str("old_room", string(oldMXID)).
		Str("new_room", string(newRoomID)).
		Str("portal_id", string(portal.ID)).
		Msg("Successfully recreated and rebound stale portal room")
	return nil
}
