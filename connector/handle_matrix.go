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
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

var _ bridgev2.ReactionHandlingNetworkAPI = (*MyNetworkClient)(nil)

var htmlTagRegex = regexp.MustCompile(`<[^>]+>`)

// HandleMatrixMessage handles incoming messages from Matrix for this user.
func (nc *MyNetworkClient) HandleMatrixMessage(ctx context.Context, msg *bridgev2.MatrixMessage) (*bridgev2.MatrixMessageResponse, error) {
	log := nc.log.With().
		Str("portal_id", string(msg.Portal.ID)).
		Str("sender_mxid", string(msg.Event.Sender)).
		Str("event_id", string(msg.Event.ID)).
		Logger()
	ctx = log.WithContext(ctx)

	log.Info().Msg("HandleMatrixMessage called")

	if !nc.isXMPPConnected() {
		return nil, fmt.Errorf("XMPP client not connected")
	}

	jid := UnescapeJID(string(msg.Portal.ID))

	meta, _ := msg.Portal.Metadata.(*PortalMetadata)
	isMUC := meta != nil && meta.IsMUC

	var msgType stanza.StanzaType = stanza.MessageTypeChat
	if isMUC {
		msgType = stanza.MessageTypeGroupchat
	}

	// Send message to XMPP
	m := stanza.NewMessage(stanza.Attrs{
		To:   jid,
		Type: msgType,
		Id:   uuid.NewString(),
	})
	extensions := make([]stanza.MsgExtension, 0, 8)
	if msg.Content.MsgType == event.MsgImage || msg.Content.MsgType == event.MsgVideo || msg.Content.MsgType == event.MsgAudio || msg.Content.MsgType == event.MsgFile {
		uploadedURL, desc, err := nc.uploadMatrixMediaToXMPP(ctx, msg)
		if err != nil {
			return nil, err
		}
		oob := stanza.OOB{URL: uploadedURL}
		if desc != "" && desc != uploadedURL {
			oob.Desc = desc
		}

		body := uploadedURL
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
		m.Body = body
	} else {
		m.Body = strings.TrimSpace(msg.Content.Body)
		if m.Body == "" && msg.Content.FormattedBody != "" {
			m.Body = strings.TrimSpace(htmlToPlainText(msg.Content.FormattedBody))
		}
		if m.Body == "" {
			return nil, fmt.Errorf("message body is empty after Matrix content conversion")
		}
	}
	if msg.Event.ID != "" {
		extensions = append(extensions, XMPPOriginID{ID: string(msg.Event.ID)})
	}
	if reply := buildOutgoingXMPPReply(msg.ReplyTo); reply != nil {
		extensions = append(extensions, reply)
		m.Body, extensions = addReplyFallbackToBody(m.Body, extensions, nc.buildOutgoingXMPPReplyQuote(ctx, msg.Portal, msg.ReplyTo))
	}
	if !isMUC {
		extensions = append(extensions, XMPPReceipt{}, XMPPMarker{})
	}
	m.Extensions = append(m.Extensions, extensions...)

	err := nc.sendXMPP(m)
	if err != nil {
		log.Err(err).Msg("Failed to send XMPP message")
		return nil, fmt.Errorf("failed to send XMPP message: %w", err)
	}

	metaLogin, _ := nc.login.Metadata.(*LoginMetadata)
	senderID := networkid.UserID(msg.Event.Sender)
	if metaLogin != nil && metaLogin.RemoteUserID != "" {
		senderID = MakeUserID(metaLogin.RemoteUserID)
	}

	return &bridgev2.MatrixMessageResponse{
		DB: &database.Message{
			ID:         networkid.MessageID(msg.Event.ID),
			SenderID:   senderID,
			SenderMXID: msg.Event.Sender,
			Timestamp:  time.UnixMilli(msg.Event.Timestamp),
		},
	}, nil
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
		if _, ok := seen[emoji]; ok {
			continue
		}
		seen[emoji] = struct{}{}
		reactions = append(reactions, emoji)
	}
	addEmoji = strings.TrimSpace(addEmoji)
	if addEmoji != "" {
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
	jid := UnescapeJID(string(portal.ID))
	meta, _ := portal.Metadata.(*PortalMetadata)
	msgType := stanza.MessageTypeChat
	if meta != nil && meta.IsMUC {
		msgType = stanza.MessageTypeGroupchat
	}
	xmppMsg := stanza.NewMessage(stanza.Attrs{
		To:   jid,
		Type: msgType,
		Id:   uuid.NewString(),
	})
	extensions := []stanza.MsgExtension{XMPPReactions{ID: string(target.ID), Reacts: reactions}}
	if reply := buildOutgoingXMPPReply(target); reply != nil {
		extensions = append(extensions, reply)
		if len(reactions) > 0 {
			xmppMsg.Body, extensions = addReplyFallbackToBody(strings.Join(reactions, " "), extensions, nc.buildOutgoingXMPPReplyQuote(ctx, portal, target))
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
	name := UnescapeJID(string(ghost.ID))
	if meta, ok := ghost.Metadata.(*GhostMetadata); ok && meta != nil && meta.CustomName != "" {
		name = meta.CustomName
	} else if parts := strings.Split(name, "/"); len(parts) > 1 && parts[len(parts)-1] != "" {
		name = parts[len(parts)-1]
	}
	return &bridgev2.UserInfo{
		Name: &name,
	}, nil
}

// GetChatInfo is not implemented for this simple connector.
func (nc *MyNetworkClient) GetChatInfo(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
	meta, _ := portal.Metadata.(*PortalMetadata)
	roomType := ptr.Ptr(database.RoomTypeDM)
	if meta != nil && meta.IsMUC {
		roomType = ptr.Ptr(database.RoomTypeDefault)
	}
	name := portalDesiredName(ctx, nc.bridge, portal, nc.contactNameSuffix())

	return &bridgev2.ChatInfo{
		Name: &name,
		Type: roomType,
	}, nil
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
