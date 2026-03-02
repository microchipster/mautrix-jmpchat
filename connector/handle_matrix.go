package connector

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mau.fi/util/ptr"
	"gosrc.io/xmpp/stanza"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
)

// HandleMatrixMessage handles incoming messages from Matrix for this user.
func (nc *MyNetworkClient) HandleMatrixMessage(ctx context.Context, msg *bridgev2.MatrixMessage) (*bridgev2.MatrixMessageResponse, error) {
	log := nc.log.With().
		Str("portal_id", string(msg.Portal.ID)).
		Str("sender_mxid", string(msg.Event.Sender)).
		Str("event_id", string(msg.Event.ID)).
		Logger()
	ctx = log.WithContext(ctx)

	log.Info().Msg("HandleMatrixMessage called")

	if nc.xmppClient == nil {
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
	})
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

		m.Extensions = append(m.Extensions, oob, fallback)
		m.Body = body
	} else {
		m.Body = msg.Content.Body
	}

	err := nc.xmppClient.Send(m)
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
	name := UnescapeJID(string(portal.ID))

	meta, _ := portal.Metadata.(*PortalMetadata)
	roomType := ptr.Ptr(database.RoomTypeDM)
	if meta != nil && meta.IsMUC {
		roomType = ptr.Ptr(database.RoomTypeDefault)
	}

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
			event.FmtBold:          event.CapLevelFullySupported,
			event.FmtItalic:        event.CapLevelFullySupported,
			event.FmtUnderline:     event.CapLevelFullySupported,
			event.FmtStrikethrough: event.CapLevelFullySupported,
			event.FmtInlineCode:    event.CapLevelFullySupported,
			event.FmtCodeBlock:     event.CapLevelFullySupported,
		},
		File: map[event.CapabilityMsgType]*event.FileFeatures{
			event.MsgImage: {
				MimeTypes: map[string]event.CapabilitySupportLevel{"*/*": event.CapLevelFullySupported},
			},
			event.MsgVideo: {
				MimeTypes: map[string]event.CapabilitySupportLevel{"*/*": event.CapLevelFullySupported},
			},
			event.MsgAudio: {
				MimeTypes: map[string]event.CapabilitySupportLevel{"*/*": event.CapLevelFullySupported},
			},
			event.MsgFile: {
				MimeTypes: map[string]event.CapabilitySupportLevel{"*/*": event.CapLevelFullySupported},
			},
		},
		Edit:   event.CapLevelFullySupported,
		Reply:  event.CapLevelFullySupported,
		Thread: event.CapLevelFullySupported,
	}
}
