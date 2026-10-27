// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"encoding/xml"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gosrc.io/xmpp/stanza"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/event"
)

var (
	_ bridgev2.EditHandlingNetworkAPI      = (*MyNetworkClient)(nil)
	_ bridgev2.RedactionHandlingNetworkAPI = (*MyNetworkClient)(nil)
)

const (
	NSMessageCorrect = "urn:xmpp:message-correct:0"
	NSRetraction     = "urn:xmpp:retraction:0"
	NSModeration     = "urn:xmpp:message-moderate:0"
)

// XMPPReplace is a XEP-0308 last message correction.
type XMPPReplace struct {
	stanza.MsgExtension
	XMLName xml.Name `xml:"urn:xmpp:message-correct:0 replace"`
	ID      string   `xml:"id,attr,omitempty"`
}

// XMPPRetract is a XEP-0424 message retraction.
type XMPPRetract struct {
	stanza.MsgExtension
	XMLName xml.Name `xml:"urn:xmpp:retraction:0 retract"`
	ID      string   `xml:"id,attr,omitempty"`
}

// XMPPRetracted is the XEP-0424 hint stanza indicating an earlier message was retracted.
type XMPPRetracted struct {
	stanza.MsgExtension
	XMLName xml.Name `xml:"urn:xmpp:retraction:0 retracted"`
	ID      string   `xml:"id,attr,omitempty"`
}

// XMPPModerated is a XEP-0425 moderation event naming the retracted room stanza-id.
type XMPPModerated struct {
	stanza.MsgExtension
	XMLName xml.Name `xml:"urn:xmpp:message-moderate:0 moderated"`
	ID      string   `xml:"id,attr,omitempty"`
	Retract string   `xml:"retract,omitempty"`
}

func init() {
	stanza.TypeRegistry.MapExtension(stanza.PKTMessage, xml.Name{Space: NSMessageCorrect, Local: "replace"}, XMPPReplace{})
	stanza.TypeRegistry.MapExtension(stanza.PKTMessage, xml.Name{Space: NSRetraction, Local: "retract"}, XMPPRetract{})
	stanza.TypeRegistry.MapExtension(stanza.PKTMessage, xml.Name{Space: NSRetraction, Local: "retracted"}, XMPPRetracted{})
	stanza.TypeRegistry.MapExtension(stanza.PKTMessage, xml.Name{Space: NSModeration, Local: "moderated"}, XMPPModerated{})
}

// xmppMessageAliases returns the identifiers carried by a stanza: the sender-chosen
// `id` attribute, the XEP-0359 origin-id and the XEP-0359 stanza-id.
func xmppMessageAliases(msg stanza.Message) (originID, stanzaID, plainID string) {
	var origin XMPPOriginID
	if msg.Get(&origin) {
		originID = strings.TrimSpace(origin.ID)
	}
	var stanzaIDExt XMPPStanzaID
	if msg.Get(&stanzaIDExt) {
		stanzaID = strings.TrimSpace(stanzaIDExt.ID)
	}
	plainID = strings.TrimSpace(msg.Id)
	return
}

const maxAliasCandidates = 5

// aliasMessageIDs finds the IDs of messages in one portal whose metadata carries
// ref under one of the given alias keys. `->>` works on Postgres jsonb and
// SQLite >= 3.38 (the `message` table column layout is pinned to the vendored
// mautrix version). A non-zero since bounds the search to rows at or after it.
func (nc *MyNetworkClient) aliasMessageIDs(ctx context.Context, portalKey networkid.PortalKey, keys []string, ref string) ([]networkid.MessageID, error) {
	return nc.aliasMessageIDsSince(ctx, portalKey, keys, ref, time.Time{})
}

// aliasMessageIDsSince is aliasMessageIDs with a lower time bound.
func (nc *MyNetworkClient) aliasMessageIDsSince(ctx context.Context, portalKey networkid.PortalKey, keys []string, ref string, since time.Time) ([]networkid.MessageID, error) {
	if ref == "" || len(keys) == 0 {
		return nil, nil
	}
	cond := ""
	for i, key := range keys {
		switch key {
		case "stanza_id", "origin_id", "xmpp_message_id":
		default:
			return nil, fmt.Errorf("unsupported metadata key %q", key)
		}
		if i > 0 {
			cond += " OR "
		}
		cond += fmt.Sprintf("metadata->>'%s'=$4", key)
	}
	window := ""
	args := []any{nc.bridge.DB.BridgeID, portalKey.ID, portalKey.Receiver, ref}
	if !since.IsZero() {
		window = " AND timestamp >= $5"
		args = append(args, since)
	}
	query := fmt.Sprintf(
		`SELECT id FROM message WHERE bridge_id=$1 AND room_id=$2 AND room_receiver=$3 AND (%s)%s ORDER BY timestamp DESC LIMIT %d`,
		cond, window, maxAliasCandidates,
	)
	rows, err := nc.bridge.DB.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []networkid.MessageID
	for rows.Next() {
		var id networkid.MessageID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// resolveXMPPRefTarget resolves an XMPP stanza reference to a bridged message
// row in the given portal. When stanzaIDOnly is set (MUC moderation), only the
// room-assigned stanza-id may match, since occupants choose their own `id`.
func (nc *MyNetworkClient) resolveXMPPRefTarget(ctx context.Context, portalKey networkid.PortalKey, ref string, stanzaIDOnly bool) (*database.Message, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" || nc.bridge == nil || nc.bridge.DB == nil {
		return nil, nil
	}
	candidates := make([]networkid.MessageID, 0, 4)
	if !stanzaIDOnly {
		candidates = append(candidates, networkid.MessageID(ref))
	}
	keys := []string{"origin_id", "xmpp_message_id", "stanza_id"}
	if stanzaIDOnly {
		keys = []string{"stanza_id"}
	}
	ids, err := nc.aliasMessageIDs(ctx, portalKey, keys, ref)
	if err != nil {
		nc.log.Warn().Err(err).Str("ref", ref).Msg("Failed to look up XMPP reference aliases")
	} else {
		candidates = append(candidates, ids...)
	}
	seen := make(map[networkid.MessageID]struct{}, len(candidates))
	for _, id := range candidates {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		parts, err := nc.bridge.DB.Message.GetAllPartsByID(ctx, portalKey.Receiver, id)
		if err != nil {
			nc.log.Warn().Err(err).Str("message_id", string(id)).Msg("Failed to load message parts for XMPP reference")
			continue
		}
		for _, part := range parts {
			if part.Room == portalKey {
				return part, nil
			}
		}
	}
	return nil, nil
}

// resolveReactionTarget maps a XEP-0444 reaction target reference to a bridged
// message row. It first tries the reference as a direct bridgev2 message id and
// falls back to the stored XMPP alias metadata.
func (nc *MyNetworkClient) resolveReactionTarget(ctx context.Context, portalKey networkid.PortalKey, ref string) (*database.Message, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" || nc.bridge == nil || nc.bridge.DB == nil {
		return nil, nil
	}
	parts, err := nc.bridge.DB.Message.GetAllPartsByID(ctx, portalKey.Receiver, networkid.MessageID(ref))
	if err != nil {
		nc.log.Warn().Err(err).Str("ref", ref).Msg("Failed to load reaction target message parts")
	} else {
		for _, part := range parts {
			if part.Room == portalKey {
				return part, nil
			}
		}
	}
	return nc.resolveXMPPRefTarget(ctx, portalKey, ref, false)
}

// HandleMatrixEdit relays a Matrix edit to XMPP. XEP-0308 corrections are sent
// for native XMPP contacts; the Cheogram/SMS gateway cannot process them, so
// those edits are delivered as a new message with a `* ` prefix.
func (nc *MyNetworkClient) HandleMatrixEdit(ctx context.Context, msg *bridgev2.MatrixEdit) error {
	if msg == nil || msg.Portal == nil || msg.EditTarget == nil {
		return nil
	}
	if !nc.isXMPPConnected() {
		return fmt.Errorf("XMPP client not connected")
	}

	// Mentions of bridged users use their XMPP names in the corrected body.
	newBody := strings.TrimSpace(rewriteMentions(msg.Content.Body, msg.Content.FormattedBody, msg.Content.Format == event.FormatHTML, nc.mentionResolver(ctx, msg.Portal)))
	if newBody == "" && msg.Content.FormattedBody != "" {
		newBody = strings.TrimSpace(htmlToPlainText(msg.Content.FormattedBody))
	}
	if newBody == "" {
		return fmt.Errorf("edited message body is empty")
	}

	jid := UnescapeJID(string(msg.Portal.ID))
	if !strings.Contains(jid, "@") {
		jid = normalizeJID(jid)
	}
	if jid == "" {
		return fmt.Errorf("portal has no valid target JID")
	}

	if meta, ok := msg.EditTarget.Metadata.(*MessageMetadata); ok && meta != nil {
		meta.Body = newBody
	} else {
		msg.EditTarget.Metadata = &MessageMetadata{Body: newBody}
	}

	msgType := outboundXMPPStanzaType(msg.Portal)

	// SMS/MMS cannot be edited: send the new text as a fresh message instead.
	if strings.HasSuffix(jid, "@cheogram.com") {
		stanzaID := uuid.NewString()
		m := stanza.NewMessage(stanza.Attrs{To: jid, Type: msgType, Id: stanzaID})
		m.Body = stanza.StringPtr("* " + newBody)
		m.Extensions = append(m.Extensions, XMPPOriginID{ID: stanzaID})
		if err := nc.sendXMPP(m); err != nil {
			return fmt.Errorf("failed to send XMPP edit fallback message: %w", err)
		}
		if !portalUsesGroupChat(msg.Portal) {
			nc.trackOutboundStanza(stanzaID, jid, msg.Portal.MXID, string(msg.Event.ID), m)
		}
		return nil
	}

	targetID := strings.TrimSpace(string(msg.EditTarget.ID))
	if targetID == "" {
		return fmt.Errorf("edit target has no XMPP message ID")
	}
	stanzaID := uuid.NewString()
	m := stanza.NewMessage(stanza.Attrs{To: jid, Type: msgType, Id: stanzaID})
	m.Body = stanza.StringPtr(newBody)
	m.Extensions = append(m.Extensions,
		XMPPReplace{ID: targetID},
		XMPPOriginID{ID: stanzaID},
	)
	if msgType == stanza.MessageTypeChat {
		if nc.simpleConfig().sendDeliveryReceiptsEnabled() {
			m.Extensions = append(m.Extensions, XMPPReceipt{})
		}
		if nc.simpleConfig().sendReadMarkersEnabled() {
			m.Extensions = append(m.Extensions, XMPPMarker{})
		}
	}
	if err := nc.sendXMPP(m); err != nil {
		return fmt.Errorf("failed to send XMPP correction: %w", err)
	}
	if msgType == stanza.MessageTypeChat {
		nc.trackOutboundStanza(stanzaID, jid, msg.Portal.MXID, string(msg.Event.ID), m)
	}
	return nil
}

// HandleMatrixMessageRemove relays a Matrix redaction to XMPP as a XEP-0424
// retraction. SMS/MMS messages cannot be unsent on the phone network, so for
// Cheogram targets a notice is posted in the portal room instead.
func (nc *MyNetworkClient) HandleMatrixMessageRemove(ctx context.Context, msg *bridgev2.MatrixMessageRemove) error {
	if msg == nil || msg.Portal == nil || msg.TargetMessage == nil {
		return nil
	}

	jid := UnescapeJID(string(msg.Portal.ID))
	if !strings.Contains(jid, "@") {
		jid = normalizeJID(jid)
	}
	if jid == "" {
		return fmt.Errorf("portal has no valid target JID")
	}

	if strings.HasSuffix(jid, "@cheogram.com") {
		if msg.Portal.MXID != "" && nc.bridge != nil && nc.bridge.Bot != nil {
			sendNotice(nc.bridge.Bot, msg.Portal.MXID, "SMS/MMS messages cannot be unsent on the phone network; the message will remain on the recipient's phone.")
		}
		return nil
	}

	if !nc.isXMPPConnected() {
		return fmt.Errorf("XMPP client not connected")
	}
	targetID := strings.TrimSpace(string(msg.TargetMessage.ID))
	if targetID == "" {
		return fmt.Errorf("redaction target has no XMPP message ID")
	}

	msgType := outboundXMPPStanzaType(msg.Portal)
	if msgType == stanza.MessageTypeGroupchat {
		// In a MUC room the room assigns the stanza-id when it echoes our
		// own messages. Retractions should reference the room-assigned ID
		// rather than our origin-id, so wait briefly for the echo. When the
		// room never echoed the message, fall back to the stored ID.
		if echo, ok := nc.waitMucEcho(ctx, targetID, mucEchoWait); ok && echo.stanzaID != "" {
			targetID = echo.stanzaID
		}
	}
	stanzaID := uuid.NewString()
	m := stanza.NewMessage(stanza.Attrs{To: jid, Type: msgType, Id: stanzaID})
	m.Extensions = append(m.Extensions,
		XMPPRetract{ID: targetID},
		XMPPOriginID{ID: stanzaID},
	)
	if err := nc.sendXMPP(m); err != nil {
		return fmt.Errorf("failed to send XMPP retraction: %w", err)
	}
	if msgType == stanza.MessageTypeChat {
		nc.trackOutboundStanza(stanzaID, jid, msg.Portal.MXID, string(msg.Event.ID), m)
	}
	return nil
}

// hasXMPPRetraction reports whether a stanza carries a XEP-0424 retraction or
// a XEP-0425 moderation event. Such stanzas usually have no body, so the empty
// body guard in the inbound pipeline must not drop them.
func hasXMPPRetraction(msg stanza.Message) bool {
	var retract XMPPRetract
	if msg.Get(&retract) && strings.TrimSpace(retract.ID) != "" {
		return true
	}
	var moderated XMPPModerated
	return msg.Get(&moderated) && strings.TrimSpace(moderated.ID) != ""
}

// handleIncomingXMPPCorrection converts a XEP-0308 correction into a Matrix edit.
// It returns true when the stanza was consumed as a correction.
func (nc *MyNetworkClient) handleIncomingXMPPCorrection(msg stanza.Message, timestamp time.Time, bareFromJid, senderID string, isMUC, isFromMe, fromMAM bool) bool {
	var replace XMPPReplace
	if !msg.Get(&replace) || strings.TrimSpace(replace.ID) == "" {
		return false
	}
	body := strings.TrimSpace(stanza.StringValue(msg.Body))
	if body == "" {
		return false
	}

	portalKey := nc.effectivePortalKey(MakePortalID(bareFromJid))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	target, err := nc.resolveXMPPRefTarget(ctx, portalKey, replace.ID, false)
	if err != nil {
		nc.log.Warn().Err(err).Str("ref", replace.ID).Msg("Failed to resolve correction target")
		return true
	}
	if target == nil {
		nc.log.Debug().Str("ref", replace.ID).Str("from", bareFromJid).Msg("Ignoring XEP-0308 correction with unknown target")
		return true
	}
	if target.SenderID != nc.senderUserID(senderID) {
		nc.log.Warn().
			Str("ref", replace.ID).
			Str("sender", senderID).
			Str("target_sender", string(target.SenderID)).
			Msg("Ignoring XEP-0308 correction from a different sender than the original message")
		return true
	}
	if meta, ok := target.Metadata.(*MessageMetadata); ok && meta != nil && meta.HasAttachment {
		nc.log.Debug().Str("ref", replace.ID).Msg("Ignoring XEP-0308 correction of a media message")
		return true
	}

	targetID := target.ID
	evt := &simplevent.Message[string]{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventEdit,
			PortalKey: networkid.PortalKey{ID: MakePortalID(bareFromJid)},
			Sender: bridgev2.EventSender{
				Sender:   nc.senderUserID(senderID),
				IsFromMe: isFromMe,
			},
			Timestamp: timestamp,
		},
		Data:          body,
		TargetMessage: targetID,
		ConvertEditFunc: func(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, existing []*database.Message, data string) (*bridgev2.ConvertedEdit, error) {
			parts := make([]*bridgev2.ConvertedEditPart, 0, len(existing))
			for _, part := range existing {
				if part.ID != targetID {
					continue
				}
				if meta, ok := part.Metadata.(*MessageMetadata); ok && meta != nil {
					meta.Body = data
				}
				parts = append(parts, &bridgev2.ConvertedEditPart{
					Part:    part,
					Type:    event.EventMessage,
					Content: &event.MessageEventContent{MsgType: event.MsgText, Body: data},
				})
			}
			if len(parts) == 0 {
				return nil, bridgev2.ErrIgnoringRemoteEvent
			}
			return &bridgev2.ConvertedEdit{ModifiedParts: parts}, nil
		},
	}
	nc.log.Debug().
		Str("from", senderID).
		Str("room_jid", bareFromJid).
		Str("target", string(targetID)).
		Bool("from_mam", fromMAM).
		Msg("Queued incoming XMPP correction event")
	nc.bridge.QueueRemoteEvent(nc.login, evt)
	return true
}

// handleIncomingXMPPRetraction converts a XEP-0424 retraction or XEP-0425
// moderation into a Matrix redaction. It returns true when the stanza was
// consumed as a retraction.
func (nc *MyNetworkClient) handleIncomingXMPPRetraction(msg stanza.Message, timestamp time.Time, bareFromJid, senderID string, isMUC, isFromMe, fromMAM bool) bool {
	var (
		ref          string
		moderation   bool
		isRetraction bool
	)
	var retract XMPPRetract
	if msg.Get(&retract) && strings.TrimSpace(retract.ID) != "" {
		ref = strings.TrimSpace(retract.ID)
		isRetraction = true
	}
	var moderated XMPPModerated
	if msg.Get(&moderated) && strings.TrimSpace(moderated.ID) != "" {
		ref = strings.TrimSpace(moderated.ID)
		moderation = true
		isRetraction = true
	}
	if !isRetraction {
		return false
	}

	portalKey := nc.effectivePortalKey(MakePortalID(bareFromJid))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	target, err := nc.resolveXMPPRefTarget(ctx, portalKey, ref, moderation)
	if err != nil {
		nc.log.Warn().Err(err).Str("ref", ref).Msg("Failed to resolve retraction target")
		return true
	}
	if target == nil {
		nc.log.Debug().Str("ref", ref).Str("from", bareFromJid).Msg("Ignoring retraction with unknown target")
		return true
	}
	if !moderation && target.SenderID != nc.senderUserID(senderID) {
		nc.log.Warn().
			Str("ref", ref).
			Str("sender", senderID).
			Str("target_sender", string(target.SenderID)).
			Msg("Ignoring retraction from a different sender than the original message")
		return true
	}

	targetID := target.ID
	evt := &simplevent.MessageRemove{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventMessageRemove,
			PortalKey: networkid.PortalKey{ID: MakePortalID(bareFromJid)},
			Sender: bridgev2.EventSender{
				Sender:   nc.senderUserID(senderID),
				IsFromMe: isFromMe,
			},
			Timestamp: timestamp,
		},
		TargetMessage: targetID,
	}
	nc.log.Debug().
		Str("from", senderID).
		Str("room_jid", bareFromJid).
		Str("target", string(targetID)).
		Bool("moderation", moderation).
		Bool("from_mam", fromMAM).
		Msg("Queued incoming XMPP retraction event")
	nc.bridge.QueueRemoteEvent(nc.login, evt)
	return true
}
