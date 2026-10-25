// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"fmt"
	"strings"

	"go.mau.fi/util/ptr"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

var (
	_ bridgev2.IdentifierResolvingNetworkAPI = (*MyNetworkClient)(nil)
	_ bridgev2.GhostDMCreatingNetworkAPI     = (*MyNetworkClient)(nil)
	_ bridgev2.GroupCreatingNetworkAPI       = (*MyNetworkClient)(nil)
	_ bridgev2.ContactListingNetworkAPI      = (*MyNetworkClient)(nil)
	_ bridgev2.IdentifierValidatingNetwork   = (*MyConnector)(nil)
)

// ResolveIdentifier implements bridgev2.IdentifierResolvingNetworkAPI.
// It resolves a phone number or JID into a ghost and, when requested, the portal key of a new DM.
func (nc *MyNetworkClient) ResolveIdentifier(ctx context.Context, identifier string, createChat bool) (*bridgev2.ResolveIdentifierResponse, error) {
	jid := normalizeJID(identifier)
	if jid == "" {
		return nil, bridgev2.RespError(mautrix.MInvalidParam.WithMessage("Please provide a phone number or a JID like user@example.com"))
	}
	if strings.Contains(jid, ",") {
		return nil, bridgev2.RespError(mautrix.MInvalidParam.WithMessage("Multiple participants cannot be resolved as a single identifier. Use the create-group command to create a group chat"))
	}

	userID := MakeUserID(jid)
	ghost, err := nc.bridge.GetGhostByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get ghost for %s: %w", jid, err)
	}

	resp := &bridgev2.ResolveIdentifierResponse{
		Ghost:  ghost,
		UserID: userID,
	}

	if userInfo, err := nc.GetUserInfo(ctx, ghost); err != nil {
		nc.log.Debug().Err(err).Str("jid", jid).Msg("Failed to get user info while resolving identifier")
	} else {
		resp.UserInfo = userInfo
	}

	if createChat {
		resp.Chat = &bridgev2.CreateChatResponse{
			PortalKey: networkid.PortalKey{
				ID: MakePortalID(jid),
			},
		}
	}

	return resp, nil
}

// CreateChatWithGhost implements bridgev2.GhostDMCreatingNetworkAPI.
// It returns the portal key of the DM with the given ghost.
func (nc *MyNetworkClient) CreateChatWithGhost(ctx context.Context, ghost *bridgev2.Ghost) (*bridgev2.CreateChatResponse, error) {
	if ghost == nil || ghost.Ghost == nil || ghost.ID == "" {
		return nil, bridgev2.RespError(mautrix.MInvalidParam.WithMessage("No valid ghost provided"))
	}
	jid := CanonicalBareJID(UnescapeJID(string(ghost.ID)))
	if jid == "" || strings.Contains(jid, ",") {
		return nil, bridgev2.RespError(mautrix.MInvalidParam.WithMessage("Cannot create a DM with %s", string(ghost.ID)))
	}
	return &bridgev2.CreateChatResponse{
		PortalKey: networkid.PortalKey{
			ID: MakePortalID(jid),
		},
	}, nil
}

// CreateGroup implements bridgev2.GroupCreatingNetworkAPI.
// It creates a Cheogram group chat with the given participants.
func (nc *MyNetworkClient) CreateGroup(ctx context.Context, params *bridgev2.GroupCreateParams) (*bridgev2.CreateChatResponse, error) {
	if params == nil {
		return nil, bridgev2.RespError(mautrix.MInvalidParam.WithMessage("No group creation parameters provided"))
	}

	participants := make([]string, 0, len(params.Participants))
	for _, participant := range params.Participants {
		participants = append(participants, CanonicalBareJID(UnescapeJID(string(participant))))
	}
	if len(participants) < 2 {
		return nil, bridgev2.RespError(mautrix.MInvalidParam.WithMessage("Creating a group chat requires at least two participants"))
	}

	name := strings.TrimSpace(ptr.Val(params.Name).Name)
	roomJID, resolvedName, err := nc.initiateGroupChat(ctx, name, participants)
	if err != nil {
		return nil, bridgev2.RespError(mautrix.MInvalidParam.WithMessage("Failed to create group chat: %v", err))
	}

	portalKey := networkid.PortalKey{ID: MakePortalID(roomJID)}
	portal, err := nc.bridge.GetPortalByKey(ctx, portalKey)
	if err != nil {
		return nil, fmt.Errorf("failed to get portal for group chat %s: %w", roomJID, err)
	}

	resp := &bridgev2.CreateChatResponse{
		PortalKey: portalKey,
		Portal:    portal,
	}

	if params.RoomID != "" {
		updateParams := bridgev2.UpdateMatrixRoomIDParams{
			SyncDBMetadata: func() {
				if name != "" {
					portal.Name = resolvedName
					portal.NameSet = true
					portal.NameIsCustom = true
				}
			},
			OverwriteOldPortal: true,
			TombstoneOldRoom:   true,
			DeleteOldRoom:      true,
			ChatInfoSource:     nc.login,
		}
		if err := portal.UpdateMatrixRoomID(ctx, params.RoomID, updateParams); err != nil {
			return nil, fmt.Errorf("failed to update portal to use the existing Matrix room: %w", err)
		}
	} else {
		info, err := nc.GetChatInfo(ctx, portal)
		if err != nil {
			nc.log.Warn().Err(err).Str("room_jid", roomJID).Msg("Failed to get chat info for new group chat")
		} else if info != nil {
			info.Name = ptr.Ptr(resolvedName)
			resp.PortalInfo = info
		}
	}

	return resp, nil
}

// GetContactList implements bridgev2.ContactListingNetworkAPI.
// It returns the contacts in the XMPP roster as resolvable identifiers.
func (nc *MyNetworkClient) GetContactList(ctx context.Context) ([]*bridgev2.ResolveIdentifierResponse, error) {
	if !nc.IsLoggedIn() {
		return nil, bridgev2.RespError(mautrix.MForbidden.WithMessage("You must be logged in to list contacts"))
	}

	jids := nc.rosterContactList()
	contacts := make([]*bridgev2.ResolveIdentifierResponse, 0, len(jids))
	for _, jid := range jids {
		userID := MakeUserID(jid)
		ghost, err := nc.bridge.GetGhostByID(ctx, userID)
		if err != nil {
			nc.log.Warn().Err(err).Str("jid", jid).Msg("Failed to get ghost while listing contacts")
			continue
		}
		contact := &bridgev2.ResolveIdentifierResponse{
			Ghost:  ghost,
			UserID: userID,
			Chat: &bridgev2.CreateChatResponse{
				PortalKey: networkid.PortalKey{ID: MakePortalID(jid)},
			},
		}
		if userInfo, err := nc.GetUserInfo(ctx, ghost); err != nil {
			nc.log.Debug().Err(err).Str("jid", jid).Msg("Failed to get user info while listing contacts")
		} else {
			contact.UserInfo = userInfo
		}
		contacts = append(contacts, contact)
	}

	return contacts, nil
}

// ValidateUserID implements bridgev2.IdentifierValidatingNetwork.
// It reports whether the given user ID is a phone number or JID this bridge can handle.
func (c *MyConnector) ValidateUserID(id networkid.UserID) bool {
	jid := CanonicalBareJID(UnescapeJID(string(id)))
	if jid == "" || strings.ContainsAny(jid, ",/ ") {
		return false
	}
	localpart, domain, ok := strings.Cut(jid, "@")
	return ok && localpart != "" && domain != ""
}
