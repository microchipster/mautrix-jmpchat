// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.mau.fi/util/ptr"
	"gosrc.io/xmpp/stanza"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/matrix"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

var _ bridgev2.ReactionHandlingNetworkAPI = (*MyNetworkClient)(nil)

var htmlTagRegex = regexp.MustCompile(`<[^>]+>`)

// HandleMatrixMessage handles incoming messages from Matrix for this user.
func (nc *MyNetworkClient) HandleMatrixMessage(ctx context.Context, msg *bridgev2.MatrixMessage) (*bridgev2.MatrixMessageResponse, error) {
	jid := UnescapeJID(string(msg.Portal.ID))
	if !strings.Contains(jid, "@") {
		jid = normalizeJID(jid)
	}

	log := nc.log.With().
		Str("portal_id", string(msg.Portal.ID)).
		Str("target_jid", jid).
		Str("sender_mxid", string(msg.Event.Sender)).
		Str("event_id", string(msg.Event.ID)).
		Str("msg_type", string(msg.Content.MsgType)).
		Logger()
	ctx = log.WithContext(ctx)

	log.Info().
		Str("portal_id", string(msg.Portal.ID)).
		Str("target_jid", jid).
		Str("sender_mxid", string(msg.Event.Sender)).
		Str("event_id", string(msg.Event.ID)).
		Str("msg_type", string(msg.Content.MsgType)).
		Msg("HandleMatrixMessage called for outbound message")

	if !nc.isXMPPConnected() {
		log.Info().Msg("XMPP client not currently connected; waiting for connection before sending message...")
		waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if !nc.WaitForConnected(waitCtx) {
			errMsg := fmt.Sprintf("Failed to send message to %s: XMPP client is not connected", jid)
			log.Warn().Msg(errMsg)
			nc.notifyManagement(errMsg)
			if msg.Portal != nil && msg.Portal.MXID != "" && nc.bridge != nil && nc.bridge.Bot != nil {
				sendNotice(nc.bridge.Bot, msg.Portal.MXID, errMsg)
			}
			return nil, bridgev2.WrapErrorInStatus(fmt.Errorf("%s", errMsg)).WithSendNotice(true)
		}
		log.Info().Msg("XMPP connection established, proceeding with message dispatch")
	}

	// Cheogram gateway JIDs (including comma-separated group texts) are not
	// MUCs and expect type=chat; real MUC rooms keep type=groupchat.
	msgType := outboundXMPPStanzaType(msg.Portal)
	isMUC := portalUsesGroupChat(msg.Portal)

	// Send message to XMPP
	m := stanza.NewMessage(stanza.Attrs{
		To:   jid,
		Type: msgType,
		Id:   uuid.NewString(),
	})
	isAttachment := msg.Content.MsgType == event.MsgImage || msg.Content.MsgType == event.MsgVideo || msg.Content.MsgType == event.MsgAudio || msg.Content.MsgType == event.MsgFile
	body := ""
	extensions := make([]stanza.MsgExtension, 0, 8)
	if isAttachment {
		uploadedURL, desc, err := nc.uploadMatrixMediaToXMPP(ctx, msg)
		if err != nil {
			log.Err(err).Msg("Failed to upload Matrix media to XMPP HTTP upload")
			return nil, err
		}
		oob := stanza.OOB{URL: uploadedURL}
		if desc != "" && desc != uploadedURL {
			oob.Desc = desc
		}

		body = uploadedURL
		fallback := XMPPFallback{For: "jabber:x:oob"}
		if desc != "" {
			body = desc + "\n" + uploadedURL
			start := len(desc) + 1
			end := start + len(uploadedURL)
			fallback.Body = []XMPPFallbackBody{{Start: start, End: end}}
		} else {
			fallback.Body = []XMPPFallbackBody{{Start: 0, End: len(uploadedURL)}}
		}

		extensions = append(extensions, oob, fallback)
		if ref := buildOutgoingXMPPAttachmentReference(msg.Content, uploadedURL); ref != nil {
			extensions = append(extensions, ref)
		}
	} else {
		// Mentions of bridged users are rewritten to their XMPP names; an HTML-only event still
		// yields plain text.
		body = strings.TrimSpace(rewriteMentions(msg.Content.Body, msg.Content.FormattedBody, msg.Content.Format == event.FormatHTML, nc.mentionResolver(ctx, msg.Portal)))
		if body == "" && msg.Content.MsgType == event.MsgLocation {
			body = strings.TrimSpace(msg.Content.GeoURI)
		}
		if body == "" && msg.Content.FormattedBody != "" {
			body = strings.TrimSpace(htmlToPlainText(msg.Content.FormattedBody))
		}
		if body == "" {
			log.Warn().Msg("Message body is empty after Matrix content conversion")
			return nil, fmt.Errorf("message body is empty after Matrix content conversion")
		}
	}
	if m.Id != "" {
		extensions = append(extensions, XMPPOriginID{ID: m.Id})
	}
	if reply := buildOutgoingXMPPReply(msg.ReplyTo); reply != nil {
		extensions = append(extensions, reply)
		body, extensions = addReplyFallbackToBody(body, extensions, nc.buildOutgoingXMPPReplyQuote(ctx, msg.Portal, msg.ReplyTo))
	}
	extensions = append(extensions, nc.xmppMentionReferences(msg.Content.Mentions)...)
	m.Body = stanza.StringPtr(body)
	if !isMUC {
		if nc.simpleConfig().sendDeliveryReceiptsEnabled() {
			extensions = append(extensions, XMPPReceipt{})
		}
		if nc.simpleConfig().sendReadMarkersEnabled() {
			extensions = append(extensions, XMPPMarker{})
		}
	}
	m.Extensions = append(m.Extensions, extensions...)

	log.Info().
		Str("to", jid).
		Str("stanza_id", m.Id).
		Str("stanza_type", string(msgType)).
		Int("body_len", len(body)).
		Int("extensions_count", len(m.Extensions)).
		Msg("Dispatching XMPP message stanza to transport")

	err := nc.sendXMPP(m)
	if err != nil {
		errMsg := fmt.Sprintf("Failed to send XMPP message to %s: %v", jid, err)
		log.Err(err).
			Str("to", jid).
			Str("stanza_id", m.Id).
			Msg("Failed to send XMPP message to stream")
		nc.notifyManagement(errMsg)
		if msg.Portal != nil && msg.Portal.MXID != "" && nc.bridge != nil && nc.bridge.Bot != nil {
			sendNotice(nc.bridge.Bot, msg.Portal.MXID, errMsg)
		}
		return nil, bridgev2.WrapErrorInStatus(fmt.Errorf("failed to send XMPP message: %w", err)).WithSendNotice(true)
	}

	if !isMUC {
		nc.trackOutboundStanza(m.Id, jid, msg.Portal.MXID, string(msg.Event.ID), m)
	}

	log.Info().
		Str("to", jid).
		Str("stanza_id", m.Id).
		Str("event_id", string(msg.Event.ID)).
		Msg("Successfully sent XMPP message to stream")

	metaLogin, _ := nc.login.Metadata.(*LoginMetadata)
	senderID := networkid.UserID(msg.Event.Sender)
	if metaLogin != nil && metaLogin.RemoteUserID != "" {
		senderID = MakeUserID(metaLogin.RemoteUserID)
	}

	return &bridgev2.MatrixMessageResponse{
		DB: &database.Message{
			ID:         networkid.MessageID(m.Id),
			SenderID:   senderID,
			SenderMXID: msg.Event.Sender,
			Timestamp:  time.UnixMilli(msg.Event.Timestamp),
			// Tapback matching needs the sent body; bridgev2 does not keep it.
			// The XMPP IDs allow later corrections/retractions to be resolved.
			Metadata: &MessageMetadata{
				Body:          body,
				HasAttachment: isAttachment,
				XMPPMessageID: m.Id,
				OriginID:      m.Id,
			},
		},
	}, nil
}

// xmppReactionSupported reports whether a Matrix reaction key can be
// represented in XMPP (XEP-0444). Image-based custom emoji (mxc:// keys)
// cannot, so they are kept Matrix-side only.
func xmppReactionSupported(emoji string) bool {
	emoji = strings.TrimSpace(emoji)
	if emoji == "" {
		return false
	}
	return !strings.HasPrefix(strings.ToLower(emoji), "mxc://")
}

// filterXMPPReactions drops reaction keys that cannot be sent over XMPP.
func filterXMPPReactions(reactions []string) []string {
	filtered := make([]string, 0, len(reactions))
	for _, reaction := range reactions {
		if xmppReactionSupported(reaction) {
			filtered = append(filtered, strings.TrimSpace(reaction))
		}
	}
	return filtered
}

func (nc *MyNetworkClient) PreHandleMatrixReaction(ctx context.Context, msg *bridgev2.MatrixReaction) (bridgev2.MatrixReactionPreResponse, error) {
	rel := msg.Content.GetRelatesTo()
	return bridgev2.MatrixReactionPreResponse{
		SenderID: nc.reactionSenderID(msg.Event.Sender),
		EmojiID:  networkid.EmojiID(rel.GetAnnotationKey()),
		Emoji:    rel.GetAnnotationKey(),
	}, nil
}

func (nc *MyNetworkClient) HandleMatrixReaction(ctx context.Context, msg *bridgev2.MatrixReaction) (*database.Reaction, error) {
	if !nc.isXMPPConnected() {
		return nil, fmt.Errorf("XMPP client not connected")
	}
	rel := msg.Content.GetRelatesTo()
	emoji := strings.TrimSpace(rel.GetAnnotationKey())
	if emoji == "" {
		return nil, fmt.Errorf("reaction emoji is empty")
	}
	if msg.TargetMessage == nil || msg.TargetMessage.ID == "" {
		return nil, fmt.Errorf("target message is missing")
	}
	if !xmppReactionSupported(emoji) {
		nc.log.Debug().
			Str("emoji", emoji).
			Msg("Storing custom emoji reaction without sending it to XMPP")
		return &database.Reaction{
			MessageID:  msg.TargetMessage.ID,
			SenderID:   nc.reactionSenderID(msg.Event.Sender),
			SenderMXID: msg.Event.Sender,
			EmojiID:    networkid.EmojiID(emoji),
			Emoji:      emoji,
			Timestamp:  time.UnixMilli(msg.Event.Timestamp),
		}, nil
	}

	reactions, err := nc.currentMatrixReactionSet(ctx, msg.Portal, msg.TargetMessage.ID, nc.reactionSenderID(msg.Event.Sender), emoji, "")
	if err != nil {
		return nil, err
	}
	if err := nc.sendXMPPReactionSet(ctx, msg.Portal, msg.TargetMessage, reactions); err != nil {
		return nil, err
	}

	return &database.Reaction{
		MessageID:  msg.TargetMessage.ID,
		SenderID:   nc.reactionSenderID(msg.Event.Sender),
		SenderMXID: msg.Event.Sender,
		EmojiID:    networkid.EmojiID(emoji),
		Emoji:      emoji,
		Timestamp:  time.UnixMilli(msg.Event.Timestamp),
	}, nil
}

func (nc *MyNetworkClient) HandleMatrixReactionRemove(ctx context.Context, msg *bridgev2.MatrixReactionRemove) error {
	if !nc.isXMPPConnected() {
		return fmt.Errorf("XMPP client not connected")
	}
	if msg.TargetReaction == nil || msg.TargetReaction.MessageID == "" {
		return fmt.Errorf("target reaction is missing")
	}
	reactions, err := nc.currentMatrixReactionSet(ctx, msg.Portal, msg.TargetReaction.MessageID, msg.TargetReaction.SenderID, "", string(msg.TargetReaction.EmojiID))
	if err != nil {
		return err
	}
	targetMessage, err := nc.bridge.DB.Message.GetLastPartByID(ctx, msg.Portal.Receiver, msg.TargetReaction.MessageID)
	if err != nil {
		return fmt.Errorf("load target message for reaction removal: %w", err)
	}
	if targetMessage == nil {
		targetMessage = &database.Message{ID: msg.TargetReaction.MessageID}
	}
	if err := nc.sendXMPPReactionSet(ctx, msg.Portal, targetMessage, reactions); err != nil {
		return err
	}
	return nil
}

func (nc *MyNetworkClient) currentMatrixReactionSet(ctx context.Context, portal *bridgev2.Portal, messageID networkid.MessageID, senderID networkid.UserID, addEmoji string, removeEmojiID string) ([]string, error) {
	if portal == nil {
		return nil, fmt.Errorf("portal is missing")
	}
	existing, err := nc.bridge.DB.Reaction.GetAllToMessageBySender(ctx, portal.Receiver, messageID, senderID)
	if err != nil {
		return nil, fmt.Errorf("load current reactions: %w", err)
	}
	seen := make(map[string]struct{}, len(existing)+1)
	reactions := make([]string, 0, len(existing)+1)
	for _, reaction := range existing {
		emojiID := string(reaction.EmojiID)
		if emojiID == "" {
			emojiID = reaction.Emoji
		}
		if removeEmojiID != "" && emojiID == removeEmojiID {
			continue
		}
		emoji := strings.TrimSpace(reaction.Emoji)
		if emoji == "" {
			emoji = strings.TrimSpace(emojiID)
		}
		if emoji == "" {
			continue
		}
		if !xmppReactionSupported(emoji) {
			continue
		}
		if _, ok := seen[emoji]; ok {
			continue
		}
		seen[emoji] = struct{}{}
		reactions = append(reactions, emoji)
	}
	addEmoji = strings.TrimSpace(addEmoji)
	if addEmoji != "" && xmppReactionSupported(addEmoji) {
		if _, ok := seen[addEmoji]; !ok {
			reactions = append(reactions, addEmoji)
		}
	}
	return reactions, nil
}

func (nc *MyNetworkClient) sendXMPPReactionSet(ctx context.Context, portal *bridgev2.Portal, target *database.Message, reactions []string) error {
	if portal == nil || target == nil || target.ID == "" {
		return fmt.Errorf("target message is missing")
	}
	filteredReactions := filterXMPPReactions(reactions)
	if len(reactions) > 0 && len(filteredReactions) == 0 {
		// Everything in the set is Matrix-only (e.g. custom emoji images);
		// sending an empty XEP-0444 set would wipe reactions on the remote side.
		return nil
	}
	reactions = filteredReactions
	jid := UnescapeJID(string(portal.ID))
	msgType := outboundXMPPStanzaType(portal)
	xmppMsg := stanza.NewMessage(stanza.Attrs{
		To:   jid,
		Type: msgType,
		Id:   uuid.NewString(),
	})
	extensions := []stanza.MsgExtension{XMPPReactions{ID: string(target.ID), Reacts: reactions}}
	if reply := buildOutgoingXMPPReply(target); reply != nil {
		extensions = append(extensions, reply)
		if len(reactions) > 0 {
			body, updatedExtensions := addReplyFallbackToBody(strings.Join(reactions, " "), extensions, nc.buildOutgoingXMPPReplyQuote(ctx, portal, target))
			xmppMsg.Body = stanza.StringPtr(body)
			extensions = updatedExtensions
			extensions = append(extensions, XMPPFallback{For: NSReactions})
		}
	}
	xmppMsg.Extensions = append(xmppMsg.Extensions, extensions...)
	if err := nc.sendXMPP(xmppMsg); err != nil {
		return fmt.Errorf("failed to send XMPP reaction: %w", err)
	}
	return nil
}

func (nc *MyNetworkClient) reactionSenderID(sender id.UserID) networkid.UserID {
	_ = sender
	metaLogin, _ := nc.login.Metadata.(*LoginMetadata)
	if metaLogin != nil && metaLogin.RemoteUserID != "" {
		return MakeUserID(metaLogin.RemoteUserID)
	}
	return networkid.UserID(sender)
}

func htmlToPlainText(input string) string {
	replacer := strings.NewReplacer(
		"<br>", "\n",
		"<br/>", "\n",
		"<br />", "\n",
		"</p>", "\n",
		"</div>", "\n",
	)
	plain := replacer.Replace(input)
	plain = htmlTagRegex.ReplaceAllString(plain, "")
	plain = strings.ReplaceAll(plain, "&nbsp;", " ")
	plain = strings.ReplaceAll(plain, "&amp;", "&")
	plain = strings.ReplaceAll(plain, "&lt;", "<")
	plain = strings.ReplaceAll(plain, "&gt;", ">")
	plain = strings.ReplaceAll(plain, "&quot;", `"`)
	plain = strings.ReplaceAll(plain, "&#39;", "'")
	lines := strings.Split(plain, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSpace(line)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// GetUserInfo is not implemented for this simple connector.
func (nc *MyNetworkClient) GetUserInfo(ctx context.Context, ghost *bridgev2.Ghost) (*bridgev2.UserInfo, error) {
	if ghost.ID == "" {
		return &bridgev2.UserInfo{}, nil
	}
	desired, customName := nc.desiredGhostName(ghost)
	if nc.isManualName(ghost.ID) {
		return &bridgev2.UserInfo{}, nil
	}
	if customName == "" && desired != ghost.Name && shouldSkipGhostNameUpdate(ghost.Name, desired, nc.liveGhostDisplayName(ctx, ghost)) {
		nc.markManualName(ghost.ID)
		return &bridgev2.UserInfo{}, nil
	}
	return &bridgev2.UserInfo{
		Name: &desired,
	}, nil
}

// desiredGhostName returns the name the bridge wants to show for a ghost,
// plus the custom name explicitly set via set-contact-name (empty when unset).
func (nc *MyNetworkClient) desiredGhostName(ghost *bridgev2.Ghost) (desired, customName string) {
	rawJID := UnescapeJID(string(ghost.ID))
	if meta, ok := ghost.Metadata.(*GhostMetadata); ok && meta != nil {
		if meta.CustomName != "" {
			customName = meta.CustomName
			desired = customName
			return
		}
		if meta.RemoteName != "" {
			desired = meta.RemoteName
			return
		}
	}
	desired = contactLabelFromJID(rawJID)
	return
}

// liveGhostDisplayName reads the display name currently set on the ghost's
// Matrix profile. It returns "" on any error or when no name is set.
func (nc *MyNetworkClient) liveGhostDisplayName(ctx context.Context, ghost *bridgev2.Ghost) string {
	mc, ok := ghost.Bridge.Matrix.(*matrix.Connector)
	if !ok || mc == nil || mc.AS == nil {
		return ""
	}
	profile, err := mc.AS.BotClient().GetProfile(ctx, ghost.Intent.GetMXID())
	if err != nil {
		return ""
	}
	return profile.DisplayName
}

func (nc *MyNetworkClient) markManualName(ghostID networkid.UserID) {
	nc.manualNameMu.Lock()
	defer nc.manualNameMu.Unlock()
	if nc.manualNames == nil {
		nc.manualNames = make(map[string]bool)
	}
	nc.manualNames[string(ghostID)] = true
}

func (nc *MyNetworkClient) isManualName(ghostID networkid.UserID) bool {
	nc.manualNameMu.Lock()
	defer nc.manualNameMu.Unlock()
	return nc.manualNames[string(ghostID)]
}

// shouldSkipGhostNameUpdate reports whether the bridge must not write a name for a
// ghost because a live Matrix profile name already exists that was not set by the bridge.
func shouldSkipGhostNameUpdate(ghostName, desired, live string) bool {
	if desired == ghostName {
		return false
	}
	return live != "" && live != ghostName
}

// GetChatInfo returns room metadata and member list for a portal.
func (nc *MyNetworkClient) GetChatInfo(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
	roomType := ptr.Ptr(database.RoomTypeDM)
	if portalUsesGroupChat(portal) {
		roomType = ptr.Ptr(database.RoomTypeDefault)
	}
	name := portalDesiredName(ctx, nc.bridge, portal, nc.contactNameSuffix(), nc.ownInitial(ctx))

	chatInfo := &bridgev2.ChatInfo{
		Name:        &name,
		Type:        roomType,
		CanBackfill: true,
	}
	if *roomType == database.RoomTypeDM {
		targetJID := UnescapeJID(string(portal.ID))
		remoteUserID := MakeUserID(targetJID)
		chatInfo.Members = &bridgev2.ChatMemberList{
			IsFull:      true,
			OtherUserID: remoteUserID,
			Members: []bridgev2.ChatMember{
				{
					EventSender: bridgev2.EventSender{
						IsFromMe: true,
					},
					Membership: event.MembershipJoin,
				},
				{
					EventSender: bridgev2.EventSender{
						IsFromMe: false,
						Sender:   remoteUserID,
					},
					Membership: event.MembershipJoin,
				},
			},
		}
		// Self-heal the portal avatar on resync from the contact's ghost.
		if nc.bridge != nil {
			if ghost, err := nc.bridge.GetExistingGhostByID(ctx, remoteUserID); err == nil && ghost != nil && ghost.AvatarMXC != "" {
				chatInfo.Avatar = &bridgev2.Avatar{
					ID:   ghost.AvatarID,
					MXC:  ghost.AvatarMXC,
					Hash: ghost.AvatarHash,
				}
			}
		}
	} else {
		targetJID := UnescapeJID(string(portal.ID))
		contacts := splitCheogramGroupContacts(targetJID)
		if len(contacts) > 0 {
			members := make([]bridgev2.ChatMember, 0, len(contacts)+1)
			members = append(members, bridgev2.ChatMember{
				EventSender: bridgev2.EventSender{
					IsFromMe: true,
				},
				Membership: event.MembershipJoin,
			})
			for _, contact := range contacts {
				members = append(members, bridgev2.ChatMember{
					EventSender: bridgev2.EventSender{
						IsFromMe: false,
						Sender:   MakeUserID(contact),
					},
					Membership: event.MembershipJoin,
				})
			}
			chatInfo.Members = &bridgev2.ChatMemberList{
				IsFull:  true,
				Members: members,
			}
		} else {
			chatInfo.Members = &bridgev2.ChatMemberList{
				IsFull: false,
				Members: []bridgev2.ChatMember{
					{
						EventSender: bridgev2.EventSender{
							IsFromMe: true,
						},
						Membership: event.MembershipJoin,
					},
				},
			}
		}
	}
	nc.extensionDecorateChatInfo(portal, chatInfo)
	return chatInfo, nil
}

// GetCapabilities returns the supported features for chats handled by this client.
func (nc *MyNetworkClient) GetCapabilities(ctx context.Context, portal *bridgev2.Portal) *event.RoomFeatures {
	return &event.RoomFeatures{
		MaxTextLength: 65536,
		Formatting: event.FormattingFeatureMap{
			event.FmtBold:          event.CapLevelUnsupported,
			event.FmtItalic:        event.CapLevelUnsupported,
			event.FmtUnderline:     event.CapLevelUnsupported,
			event.FmtStrikethrough: event.CapLevelUnsupported,
			event.FmtInlineCode:    event.CapLevelUnsupported,
			event.FmtCodeBlock:     event.CapLevelUnsupported,
		},
		File: map[event.CapabilityMsgType]*event.FileFeatures{
			event.MsgImage: {
				MimeTypes: map[string]event.CapabilitySupportLevel{"*/*": event.CapLevelFullySupported},
				Caption:   event.CapLevelFullySupported,
			},
			event.MsgVideo: {
				MimeTypes: map[string]event.CapabilitySupportLevel{"*/*": event.CapLevelFullySupported},
				Caption:   event.CapLevelFullySupported,
			},
			event.MsgAudio: {
				MimeTypes: map[string]event.CapabilitySupportLevel{"*/*": event.CapLevelFullySupported},
				Caption:   event.CapLevelFullySupported,
			},
			event.CapMsgVoice: {
				MimeTypes: map[string]event.CapabilitySupportLevel{"*/*": event.CapLevelFullySupported},
				Caption:   event.CapLevelFullySupported,
			},
			event.MsgFile: {
				MimeTypes: map[string]event.CapabilitySupportLevel{"*/*": event.CapLevelFullySupported},
				Caption:   event.CapLevelFullySupported,
			},
		},
		Edit:     event.CapLevelUnsupported,
		Reply:    event.CapLevelPartialSupport,
		Thread:   event.CapLevelUnsupported,
		Reaction: event.CapLevelPartialSupport,
	}
}
