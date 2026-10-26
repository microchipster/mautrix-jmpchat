// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"gosrc.io/xmpp/stanza"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
)

var _ bridgev2.TypingHandlingNetworkAPI = (*MyNetworkClient)(nil)

// xmppTypingTimeout is how long Matrix should show a typing indicator after
// an inbound XMPP "composing" chat state, in case the matching "paused" state
// never arrives.
const xmppTypingTimeout = 30 * time.Second

// HandleMatrixTyping sends an XEP-0085 chat state to XMPP for Matrix typing
// notifications. Chat states are best-effort: failures are logged and ignored,
// and they are never sent to Cheogram gateway chats (SMS/MMS cannot carry them).
func (nc *MyNetworkClient) HandleMatrixTyping(ctx context.Context, msg *bridgev2.MatrixTyping) error {
	if msg == nil || msg.Portal == nil || msg.Type != bridgev2.TypingTypeText {
		return nil
	}
	if !nc.isXMPPConnected() {
		return nil
	}
	if !nc.simpleConfig().sendTypingEnabled() {
		return nil
	}

	jid := UnescapeJID(string(msg.Portal.ID))
	if !strings.Contains(jid, "@") {
		jid = normalizeJID(jid)
	}
	if jid == "" || !shouldSendXMPPTyping(jid) {
		return nil
	}

	msgType := stanza.MessageTypeChat
	if portalUsesGroupChat(msg.Portal) {
		msgType = stanza.MessageTypeGroupchat
	}

	m := stanza.NewMessage(stanza.Attrs{
		To:   jid,
		Type: msgType,
		Id:   uuid.NewString(),
	})
	var state stanza.MsgExtension = stanza.StateComposing{}
	if !msg.IsTyping {
		state = stanza.StatePaused{}
	}
	m.Extensions = append(m.Extensions, state)

	if err := nc.sendXMPP(m); err != nil {
		nc.log.Debug().
			Err(err).
			Str("to", jid).
			Bool("is_typing", msg.IsTyping).
			Msg("Failed to send XMPP chat state")
	}
	return nil
}

// shouldSendXMPPTyping reports whether chat states make sense for a target JID.
// The Cheogram/JMP gateway bridges SMS/MMS, which cannot carry typing
// indicators, so chat states are suppressed for all cheogram.com targets.
func shouldSendXMPPTyping(jid string) bool {
	jid = strings.ToLower(strings.TrimSpace(jid))
	return jid != "" && !strings.HasSuffix(jid, "@cheogram.com")
}

// extractXMPPChatState maps XEP-0085 chat state extensions to an isTyping flag.
// The second return value reports whether any chat state was present.
func extractXMPPChatState(msg stanza.Message) (isTyping, ok bool) {
	var active stanza.StateActive
	if msg.Get(&active) {
		return false, true
	}
	var composing stanza.StateComposing
	if msg.Get(&composing) {
		return true, true
	}
	var paused stanza.StatePaused
	if msg.Get(&paused) {
		return false, true
	}
	var inactive stanza.StateInactive
	if msg.Get(&inactive) {
		return false, true
	}
	var gone stanza.StateGone
	if msg.Get(&gone) {
		return false, true
	}
	return false, false
}

// handleIncomingXMPPTyping converts XEP-0085 chat states into bridgev2 typing
// events. It returns true when the stanza was consumed as a chat state, so the
// caller stops processing it as a regular message.
func (nc *MyNetworkClient) handleIncomingXMPPTyping(msg stanza.Message, bareFromJid string, isMUC, isFromMe, fromMAM bool) bool {
	if fromMAM || isFromMe || bareFromJid == "" {
		return false
	}
	isTyping, ok := extractXMPPChatState(msg)
	if !ok {
		return false
	}
	// A body means this is a normal message that happens to carry a chat state.
	if stanza.StringValue(msg.Body) != "" {
		return false
	}
	if !shouldSendXMPPTyping(bareFromJid) {
		return true
	}

	senderJID := bareFromJid
	if isMUC {
		senderJID = nc.resolveMUCSenderID(msg, bareFromJid)
		if senderJID == "" {
			return true
		}
	}

	timeout := xmppTypingTimeout
	if !isTyping {
		timeout = 0
	}
	evt := &simplevent.Typing{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventTyping,
			PortalKey: networkid.PortalKey{ID: MakePortalID(bareFromJid)},
			Sender: bridgev2.EventSender{
				Sender: nc.senderUserID(senderJID),
			},
			Timestamp: time.Now(),
		},
		Timeout: timeout,
		Type:    bridgev2.TypingTypeText,
	}
	nc.log.Debug().
		Str("from", senderJID).
		Str("room_jid", bareFromJid).
		Bool("is_typing", isTyping).
		Msg("Queued incoming XMPP typing event")
	nc.bridge.QueueRemoteEvent(nc.login, evt)
	return true
}
