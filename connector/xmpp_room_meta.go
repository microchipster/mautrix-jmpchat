// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"gosrc.io/xmpp/stanza"
	"maunium.net/go/mautrix/bridgev2"
)

var (
	_ bridgev2.RoomNameHandlingNetworkAPI  = (*MyNetworkClient)(nil)
	_ bridgev2.RoomTopicHandlingNetworkAPI = (*MyNetworkClient)(nil)
)

// mucNickFromRoomName derives a MUC nickname from a Matrix room name. Names
// that are empty or contain whitespace are not nickname-like and are skipped,
// since rooms are usually named after their participants rather than a nick.
func mucNickFromRoomName(name string) (string, bool) {
	nick := normalizeContactName(name)
	if nick == "" || strings.ContainsAny(nick, " \t") {
		return "", false
	}
	return nick, true
}

// HandleMatrixRoomName persists user renames of portal rooms. For MUC rooms
// with nickname sync enabled the new name is also applied as the occupant
// nickname, matching the reference bridge's display-name <-> nick behavior.
func (nc *MyNetworkClient) HandleMatrixRoomName(ctx context.Context, msg *bridgev2.MatrixRoomName) (bool, error) {
	if msg == nil || msg.Portal == nil || msg.Content == nil {
		return false, nil
	}
	portal := msg.Portal
	name := normalizeContactName(msg.Content.Name)
	if name == "" {
		return false, nil
	}
	roomJID := UnescapeJID(string(portal.ID))
	if roomJID != "" && nc.simpleConfig().mucNickSyncEnabled() && nc.isXMPPConnected() {
		meta, _ := portal.Metadata.(*PortalMetadata)
		if meta != nil && meta.IsMUC {
			if nick, ok := mucNickFromRoomName(name); ok {
				if !strings.EqualFold(nc.mucNickOr(roomJID), nick) {
					if err := nc.setMUCNickOverride(ctx, roomJID, nick); err != nil {
						nc.log.Warn().Err(err).Str("room_jid", roomJID).Msg("Failed to persist MUC nickname override from room rename")
					}
					nc.sendMUCNickChange(roomJID, nick)
				}
			} else {
				nc.log.Debug().Str("room_jid", roomJID).Str("name", name).Msg("Skipping MUC nickname sync: room name is not nickname-like")
			}
		}
	}
	portal.Name = name
	portal.NameSet = true
	portal.NameIsCustom = true
	return true, nil
}

// HandleMatrixRoomTopic forwards Matrix topic changes to group chats as MUC
// subject changes (XEP-0045), allowing other participants to see the topic.
func (nc *MyNetworkClient) HandleMatrixRoomTopic(ctx context.Context, msg *bridgev2.MatrixRoomTopic) (bool, error) {
	if msg == nil || msg.Portal == nil || msg.Content == nil {
		return false, nil
	}
	portal := msg.Portal
	topic := strings.TrimSpace(msg.Content.Topic)
	roomJID := UnescapeJID(string(portal.ID))
	if roomJID != "" && nc.isXMPPConnected() && portalUsesGroupChat(portal) {
		m := stanza.NewMessage(stanza.Attrs{To: roomJID, Type: stanza.MessageTypeGroupchat, Id: uuid.NewString()})
		m.Subject = stanza.StringPtr(topic)
		if err := nc.sendXMPP(m); err != nil {
			nc.log.Warn().Err(err).Str("room_jid", roomJID).Msg("Failed to send MUC subject change")
		}
	}
	portal.Topic = topic
	portal.TopicSet = true
	return true, nil
}
