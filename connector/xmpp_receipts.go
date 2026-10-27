package connector

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gosrc.io/xmpp/stanza"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
)

func extractXMPPReceiptTarget(msg stanza.Message) (bridgev2.RemoteEventType, string, bool) {
	var receipt XMPPReceiptReceived
	if msg.Get(&receipt) && receipt.ID != "" {
		return bridgev2.RemoteEventDeliveryReceipt, receipt.ID, true
	}

	var markerReceived XMPPMarkerReceived
	if msg.Get(&markerReceived) && markerReceived.ID != "" {
		return bridgev2.RemoteEventDeliveryReceipt, markerReceived.ID, true
	}

	var markerDisplayed XMPPMarkerDisplayed
	if msg.Get(&markerDisplayed) && markerDisplayed.ID != "" {
		return bridgev2.RemoteEventReadReceipt, markerDisplayed.ID, true
	}

	var markerAcknowledged XMPPMarkerAcknowledged
	if msg.Get(&markerAcknowledged) && markerAcknowledged.ID != "" {
		return bridgev2.RemoteEventReadReceipt, markerAcknowledged.ID, true
	}

	return 0, "", false
}

func (nc *MyNetworkClient) handleIncomingXMPPReceipt(msg stanza.Message, timestamp time.Time, bareFromJid string, isMUC bool, isFromMe bool) bool {
	if isMUC {
		return false
	}

	receiptType, targetID, ok := extractXMPPReceiptTarget(msg)
	if !ok {
		return false
	}

	ackKind := "read"
	if receiptType == bridgev2.RemoteEventDeliveryReceipt {
		ackKind = "delivery"
	}
	nc.resolveOutboundAck(targetID, ackKind, msg.From)

	senderID := bareFromJid
	if isFromMe {
		if meta, ok := nc.login.Metadata.(*LoginMetadata); ok && meta != nil && meta.RemoteUserID != "" {
			senderID = meta.RemoteUserID
		}
	}

	nc.bridge.QueueRemoteEvent(nc.login, &simplevent.Receipt{
		EventMeta: simplevent.EventMeta{
			Type:      receiptType,
			PortalKey: networkid.PortalKey{ID: MakePortalID(bareFromJid)},
			Sender: bridgev2.EventSender{
				Sender:   nc.senderUserID(senderID),
				IsFromMe: isFromMe,
			},
			Timestamp: timestamp,
		},
		LastTarget: networkid.MessageID(targetID),
		Targets:    []networkid.MessageID{networkid.MessageID(targetID)},
		ReadUpTo:   timestamp,
	})

	return true
}

func (nc *MyNetworkClient) respondToIncomingXMPPReceipt(msg stanza.Message, isFromMe, fromMAM bool) {
	response, ok := buildIncomingXMPPReceiptResponse(msg, isFromMe, fromMAM)
	if !ok {
		return
	}
	if err := nc.sendXMPP(response); err != nil {
		nc.log.Err(err).Str("to", msg.From).Str("id", msg.Id).Msg("Failed to acknowledge XMPP receipt request")
	}
}

func buildIncomingXMPPReceiptResponse(msg stanza.Message, isFromMe, fromMAM bool) (stanza.Message, bool) {
	if fromMAM || isFromMe || msg.Id == "" || msg.From == "" {
		return stanza.Message{}, false
	}
	var request XMPPReceipt
	if !msg.Get(&request) {
		return stanza.Message{}, false
	}
	response := stanza.NewMessage(stanza.Attrs{
		To:   msg.From,
		Type: msg.Type,
		Id:   uuid.NewString(),
	})
	response.Extensions = append(response.Extensions, &XMPPReceiptReceived{ID: msg.Id})
	return response, true
}

var _ bridgev2.ReadReceiptHandlingNetworkAPI = (*MyNetworkClient)(nil)

func (nc *MyNetworkClient) HandleMatrixReadReceipt(ctx context.Context, msg *bridgev2.MatrixReadReceipt) error {
	if !nc.isXMPPConnected() {
		return fmt.Errorf("XMPP client not connected")
	}
	if msg == nil || msg.Portal == nil || msg.ExactMessage == nil || msg.ExactMessage.ID == "" {
		return nil
	}
	if !msg.LastRead.IsZero() && !msg.ReadUpTo.After(msg.LastRead) {
		return nil
	}

	if portalUsesGroupChat(msg.Portal) {
		return nil
	}

	xmppMsg := stanza.NewMessage(stanza.Attrs{
		To:   UnescapeJID(string(msg.Portal.ID)),
		Type: outboundXMPPStanzaType(msg.Portal),
		Id:   uuid.NewString(),
	})
	xmppMsg.Extensions = append(xmppMsg.Extensions,
		XMPPReceiptReceived{ID: string(msg.ExactMessage.ID)},
		XMPPMarkerDisplayed{ID: string(msg.ExactMessage.ID)},
	)

	return nc.sendXMPP(xmppMsg)
}
