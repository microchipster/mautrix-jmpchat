// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gosrc.io/xmpp/stanza"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/event"
)

type xmppCallEvent struct {
	Action    string
	SessionID string
	Reason    string
}

type xmppCallSessionState struct {
	InitiatedByMe bool
	ProposedAt    time.Time
	AnsweredAt    time.Time
	LastUpdatedAt time.Time
	LastAction    string
	LastNotice    string
	WasRinging    bool
	WasAnswered   bool
	Terminal      bool
}

func extractXMPPCallEvent(msg stanza.Message) *xmppCallEvent {
	for _, ext := range msg.Extensions {
		switch typed := ext.(type) {
		case *XMPPJinglePropose:
			if typed.ID != "" && typed.HasRTPDescription() {
				return &xmppCallEvent{Action: "propose", SessionID: strings.TrimSpace(typed.ID)}
			}
		case XMPPJinglePropose:
			if typed.ID != "" && typed.HasRTPDescription() {
				return &xmppCallEvent{Action: "propose", SessionID: strings.TrimSpace(typed.ID)}
			}
		case *XMPPJingleProceed:
			if typed.ID != "" {
				return &xmppCallEvent{Action: "proceed", SessionID: strings.TrimSpace(typed.ID)}
			}
		case XMPPJingleProceed:
			if typed.ID != "" {
				return &xmppCallEvent{Action: "proceed", SessionID: strings.TrimSpace(typed.ID)}
			}
		case *XMPPJingleAccept:
			if typed.ID != "" {
				return &xmppCallEvent{Action: "accept", SessionID: strings.TrimSpace(typed.ID)}
			}
		case XMPPJingleAccept:
			if typed.ID != "" {
				return &xmppCallEvent{Action: "accept", SessionID: strings.TrimSpace(typed.ID)}
			}
		case *XMPPJingleReject:
			if typed.ID != "" {
				return &xmppCallEvent{Action: "reject", SessionID: strings.TrimSpace(typed.ID)}
			}
		case XMPPJingleReject:
			if typed.ID != "" {
				return &xmppCallEvent{Action: "reject", SessionID: strings.TrimSpace(typed.ID)}
			}
		case *XMPPJingleRetract:
			if typed.ID != "" {
				return &xmppCallEvent{Action: "retract", SessionID: strings.TrimSpace(typed.ID)}
			}
		case XMPPJingleRetract:
			if typed.ID != "" {
				return &xmppCallEvent{Action: "retract", SessionID: strings.TrimSpace(typed.ID)}
			}
		case *XMPPJingleRinging:
			if typed.ID != "" {
				return &xmppCallEvent{Action: "ringing", SessionID: strings.TrimSpace(typed.ID)}
			}
		case XMPPJingleRinging:
			if typed.ID != "" {
				return &xmppCallEvent{Action: "ringing", SessionID: strings.TrimSpace(typed.ID)}
			}
		case *XMPPJingleFinish:
			if typed.ID != "" {
				return &xmppCallEvent{Action: "finish", SessionID: strings.TrimSpace(typed.ID), Reason: typed.ReasonName()}
			}
		case XMPPJingleFinish:
			if typed.ID != "" {
				return &xmppCallEvent{Action: "finish", SessionID: strings.TrimSpace(typed.ID), Reason: typed.ReasonName()}
			}
		}
	}
	return nil
}

func advanceXMPPCallSession(session *xmppCallSessionState, call *xmppCallEvent, timestamp time.Time, isFromMe bool) (*xmppCallSessionState, string) {
	if call == nil {
		return session, ""
	}
	if session == nil {
		session = &xmppCallSessionState{}
	} else if session.Terminal {
		return session, ""
	}
	if session.ProposedAt.IsZero() {
		session.ProposedAt = timestamp
	}
	session.LastUpdatedAt = timestamp
	notice := ""
	switch call.Action {
	case "propose":
		if session.LastAction != "propose" {
			session.InitiatedByMe = isFromMe
			session.ProposedAt = timestamp
			if isFromMe {
				notice = "Outgoing call"
			} else {
				notice = "Incoming call"
			}
		}
	case "ringing":
		if !session.WasRinging {
			session.WasRinging = true
			session.InitiatedByMe = true
			notice = "Phone ringing"
		}
	case "proceed", "accept":
		if !session.WasAnswered {
			session.WasAnswered = true
			session.AnsweredAt = timestamp
			notice = "Call answered"
		}
	case "reject":
		session.Terminal = true
		notice = rejectNoticeForSession(session, isFromMe)
	case "retract":
		session.Terminal = true
		notice = retractNoticeForSession(session)
	case "finish":
		session.Terminal = true
		notice = finishNoticeForSession(session, call, timestamp, isFromMe)
	default:
		return session, ""
	}
	if notice == session.LastNotice {
		notice = ""
	}
	if notice != "" {
		session.LastNotice = notice
	}
	session.LastAction = call.Action
	return session, notice
}

func formatCallNoticeForParty(notice, party string) string {
	party = strings.TrimSpace(party)
	if party == "" {
		notice = strings.ReplaceAll(notice, " (peer hung up)", " (ended by other party)")
		notice = strings.ReplaceAll(notice, " (peer declined)", " (declined by other party)")
		return notice
	}
	switch {
	case notice == "Incoming call":
		return fmt.Sprintf("Incoming call from %s", party)
	case notice == "Incoming call cancelled":
		return fmt.Sprintf("%s cancelled the call", party)
	case notice == "Outgoing call":
		return fmt.Sprintf("Calling %s", party)
	case notice == "Phone ringing":
		return fmt.Sprintf("%s is ringing", party)
	case notice == "Call answered":
		return fmt.Sprintf("Call with %s answered", party)
	case notice == "Call busy":
		return fmt.Sprintf("%s is busy", party)
	case notice == "Call timed out":
		return fmt.Sprintf("%s didn't answer", party)
	case notice == "Missed call":
		return fmt.Sprintf("Missed call from %s", party)
	case notice == "Outgoing call cancelled":
		return fmt.Sprintf("Call to %s cancelled", party)
	case notice == "Call declined":
		return fmt.Sprintf("%s declined the call", party)
	case notice == "Declined incoming call":
		return fmt.Sprintf("Declined call from %s", party)
	case notice == "Call ended":
		return fmt.Sprintf("Call with %s ended", party)
	case notice == "Outgoing call ended":
		return fmt.Sprintf("Call with %s ended", party)
	case strings.HasPrefix(notice, "Call ended"):
		detail := strings.TrimPrefix(notice, "Call ended")
		detail = strings.ReplaceAll(detail, "(peer hung up)", fmt.Sprintf("(%s hung up)", party))
		detail = strings.ReplaceAll(detail, "(peer declined)", fmt.Sprintf("(%s declined)", party))
		return fmt.Sprintf("Call with %s ended%s", party, detail)
	default:
		return notice
	}
}

func (nc *MyNetworkClient) resolveCallPartyLabel(ctx context.Context, bareFromJid string) string {
	label := callPartyLabelFromJID(bareFromJid)
	ghost, err := nc.bridge.GetExistingGhostByID(ctx, MakeUserID(bareFromJid))
	if err != nil || ghost == nil {
		return label
	}
	if meta, ok := ghost.Metadata.(*GhostMetadata); ok && meta != nil {
		if meta.CustomName != "" {
			return meta.CustomName
		}
		if meta.RemoteName != "" {
			return meta.RemoteName
		}
	}
	if ghost.Name != "" {
		return ghost.Name
	}
	return label
}

func callPartyLabelFromJID(jid string) string {
	return contactLabelFromJID(jid)
}

func rejectNoticeForSession(session *xmppCallSessionState, isFromMe bool) string {
	if session.WasAnswered {
		if isFromMe {
			return "Call ended (you hung up)"
		}
		return "Call ended (peer hung up)"
	}
	if session.InitiatedByMe {
		return "Call declined"
	}
	if isFromMe {
		return "Declined incoming call"
	}
	return "Missed call"
}

func retractNoticeForSession(session *xmppCallSessionState) string {
	if session.WasAnswered {
		return "Call ended"
	}
	if session.InitiatedByMe {
		return "Outgoing call cancelled"
	}
	return "Incoming call cancelled"
}

func finishNoticeForSession(session *xmppCallSessionState, call *xmppCallEvent, timestamp time.Time, isFromMe bool) string {
	if session.WasAnswered {
		durationStr := ""
		if duration := callDuration(session, timestamp); duration > 0 {
			durationStr = fmt.Sprintf(" after %s", formatCallDuration(duration))
		}
		var tag string
		switch call.Reason {
		case "busy":
			tag = " (busy)"
		case "decline":
			if isFromMe {
				tag = " (you declined)"
			} else {
				tag = " (peer declined)"
			}
		case "connectivity-error", "media-error":
			tag = " (connection lost)"
		default:
			if isFromMe {
				tag = " (you hung up)"
			} else {
				tag = " (peer hung up)"
			}
		}
		return fmt.Sprintf("Call ended%s%s", durationStr, tag)
	}
	switch call.Reason {
	case "busy", "decline":
		if session.InitiatedByMe {
			if call.Reason == "busy" {
				return "Call busy"
			}
			return "Call declined"
		}
		if isFromMe {
			return "Declined incoming call"
		}
		return "Missed call"
	case "cancel", "timeout":
		if call.Reason == "timeout" {
			if session.InitiatedByMe {
				return "Call timed out"
			}
			return "Missed call"
		}
		return retractNoticeForSession(session)
	case "success":
		if isFromMe {
			return "Call ended (you hung up)"
		}
		return "Call ended (peer hung up)"
	default:
		if session.InitiatedByMe {
			return "Outgoing call ended"
		}
		return "Missed call"
	}
}

func callDuration(session *xmppCallSessionState, timestamp time.Time) time.Duration {
	if session == nil || session.AnsweredAt.IsZero() || timestamp.Before(session.AnsweredAt) {
		return 0
	}
	return timestamp.Sub(session.AnsweredAt).Round(time.Second)
}

func formatCallDuration(duration time.Duration) string {
	if duration <= 0 {
		return "0s"
	}
	seconds := int(duration / time.Second)
	hours := seconds / 3600
	minutes := (seconds % 3600) / 60
	secs := seconds % 60
	parts := make([]string, 0, 3)
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if minutes > 0 {
		parts = append(parts, fmt.Sprintf("%dm", minutes))
	}
	if secs > 0 || len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%ds", secs))
	}
	return strings.Join(parts, " ")
}

func (nc *MyNetworkClient) getAndAdvanceCallSession(key string, call *xmppCallEvent, timestamp time.Time, isFromMe bool) string {
	nc.callSessionsMu.Lock()
	defer nc.callSessionsMu.Unlock()
	if nc.callSessions == nil {
		nc.callSessions = map[string]*xmppCallSessionState{}
	}
	nc.pruneCallSessionsLocked(timestamp)
	session := nc.callSessions[key]
	updated, notice := advanceXMPPCallSession(session, call, timestamp, isFromMe)
	if updated == nil {
		delete(nc.callSessions, key)
		return notice
	}
	if updated.Terminal && (session == nil || !session.Terminal) {
		nc.recordJMICallHistory(key, call, updated, timestamp)
	}
	nc.callSessions[key] = updated
	return notice
}

// recordJMICallHistory stores a finished XEP-0353 call in the login's bounded
// call log so users can see recent calls without opening Cheogram.
func (nc *MyNetworkClient) recordJMICallHistory(key string, call *xmppCallEvent, session *xmppCallSessionState, timestamp time.Time) {
	peer := key
	if idx := strings.IndexByte(peer, '|'); idx >= 0 {
		peer = peer[:idx]
	}
	entry := CallHistoryEntry{
		Number:    peer,
		Direction: "incoming",
		Status:    jmiCallStatus(call),
		StartedAt: session.ProposedAt,
	}
	if session.InitiatedByMe {
		entry.Direction = "outgoing"
		if entry.Status == "missed" {
			entry.Status = "cancelled"
		}
	}
	if session.WasAnswered && !session.AnsweredAt.IsZero() && timestamp.After(session.AnsweredAt) {
		entry.DurationSeconds = int(timestamp.Sub(session.AnsweredAt).Seconds())
	}
	if entry.StartedAt.IsZero() {
		entry.StartedAt = timestamp
	}
	go nc.recordCallHistoryForCurrentLogin(entry)
}

// jmiCallStatus turns the terminal call event into a short history status.
func jmiCallStatus(call *xmppCallEvent) string {
	switch call.Action {
	case "finish":
		switch call.Reason {
		case "":
			return "ended"
		case "success":
			return "completed"
		}
		return call.Reason
	case "reject":
		if call.Reason != "" {
			return call.Reason
		}
		return "rejected"
	case "retract":
		return "missed"
	}
	if call.Reason != "" {
		return call.Reason
	}
	return call.Action
}

func (nc *MyNetworkClient) recordCallHistoryForCurrentLogin(entry CallHistoryEntry) {
	if nc.login == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	recordCallHistory(ctx, nc.login, entry)
}

func (nc *MyNetworkClient) pruneCallSessionsLocked(now time.Time) {
	const maxCallSessionAge = 6 * time.Hour
	for key, session := range nc.callSessions {
		if session == nil {
			delete(nc.callSessions, key)
			continue
		}
		reference := session.LastUpdatedAt
		if reference.IsZero() {
			reference = session.ProposedAt
		}
		if !reference.IsZero() && now.Sub(reference) > maxCallSessionAge {
			delete(nc.callSessions, key)
		}
	}
}

func (nc *MyNetworkClient) handleIncomingXMPPCall(msg stanza.Message, timestamp time.Time, bareFromJid string, isMUC bool, isFromMe bool) bool {
	if isMUC {
		return false
	}
	if !isFromMe && nc.extensionSuppressCallNotice(bareFromJid) {
		// The SIP calls extension rings this call itself; do not also post JMI notices.
		return true
	}
	call := extractXMPPCallEvent(msg)
	if call == nil {
		return false
	}
	body := nc.getAndAdvanceCallSession(bareFromJid+"|"+call.SessionID, call, timestamp, isFromMe)
	if body == "" {
		return true
	}
	body = formatCallNoticeForParty(body, nc.resolveCallPartyLabel(context.Background(), bareFromJid))
	portalID := MakePortalID(bareFromJid)
	senderID := bareFromJid
	if isFromMe {
		if meta, ok := nc.login.Metadata.(*LoginMetadata); ok && meta != nil && meta.RemoteUserID != "" {
			senderID = meta.RemoteUserID
		}
	}
	resync := &simplevent.ChatResync{
		EventMeta: simplevent.EventMeta{
			Type:         bridgev2.RemoteEventChatResync,
			PortalKey:    networkid.PortalKey{ID: portalID},
			CreatePortal: true,
			Timestamp:    timestamp,
		},
		ChatInfo: nil,
	}
	nc.bridge.QueueRemoteEvent(nc.login, resync)
	msgID := nc.extractMessageID(msg, bareFromJid, isFromMe)
	if msgID == "" {
		msgID = fmt.Sprintf("call-%s-%s", call.Action, call.SessionID)
	}
	nc.bridge.QueueRemoteEvent(nc.login, &simplevent.Message[string]{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventMessage,
			PortalKey: networkid.PortalKey{ID: portalID},
			Sender: bridgev2.EventSender{
				Sender:   nc.senderUserID(senderID),
				IsFromMe: isFromMe,
			},
			Timestamp: timestamp,
		},
		Data: body,
		ID:   networkid.MessageID(msgID),
		ConvertMessageFunc: func(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, data string) (*bridgev2.ConvertedMessage, error) {
			return &bridgev2.ConvertedMessage{Parts: []*bridgev2.ConvertedMessagePart{{
				Type: event.EventMessage,
				Content: &event.MessageEventContent{
					MsgType: event.MsgNotice,
					Body:    data,
				},
			}}}, nil
		},
	})
	return true
}
