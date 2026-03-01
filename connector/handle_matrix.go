package connector

import (
	"context"
	"fmt"
	"strings"

	"go.mau.fi/util/ptr"
	"gosrc.io/xmpp/stanza"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
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

	jid := string(msg.Portal.ID)

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
	m.Body = msg.Content.Body

	err := nc.xmppClient.Send(m)
	if err != nil {
		log.Err(err).Msg("Failed to send XMPP message")
		return nil, fmt.Errorf("failed to send XMPP message: %w", err)
	}

	return &bridgev2.MatrixMessageResponse{}, nil
}

// GetUserInfo is not implemented for this simple connector.
func (nc *MyNetworkClient) GetUserInfo(ctx context.Context, ghost *bridgev2.Ghost) (*bridgev2.UserInfo, error) {
	name := string(ghost.ID)
	if parts := strings.Split(name, "/"); len(parts) > 1 && parts[len(parts)-1] != "" {
		name = parts[len(parts)-1]
	}
	return &bridgev2.UserInfo{
		Name: &name,
	}, nil
}

// GetChatInfo is not implemented for this simple connector.
func (nc *MyNetworkClient) GetChatInfo(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
	name := string(portal.ID)

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
		Edit:   event.CapLevelFullySupported,
		Reply:  event.CapLevelFullySupported,
		Thread: event.CapLevelFullySupported,
	}
}
