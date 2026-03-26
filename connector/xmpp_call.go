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
		notice = finishNoticeForSession(session, call, timestamp)
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
	case notice == "Call ended":
		return fmt.Sprintf("Call with %s ended", party)
	case notice == "Outgoing call ended":
		return fmt.Sprintf("Call with %s ended", party)
	case strings.HasPrefix(notice, "Call ended after "):
		return fmt.Sprintf("Call with %s ended after %s", party, strings.TrimPrefix(notice, "Call ended after "))
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
	jid = strings.TrimSpace(jid)
	if jid == "" {
		return ""
	}
	if parts := strings.SplitN(jid, "@", 2); len(parts) == 2 && strings.HasPrefix(parts[0], "+") {
		return parts[0]
	}
	if parts := strings.SplitN(jid, "/", 2); len(parts) == 2 && parts[1] != "" {
		return parts[1]
	}
	return jid
}

func rejectNoticeForSession(session *xmppCallSessionState, isFromMe bool) string {
	if session.WasAnswered {
		return "Call ended"
	}
	if session.InitiatedByMe {
		return "Call declined"
	}
	if isFromMe {
		return "Call declined"
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

func finishNoticeForSession(session *xmppCallSessionState, call *xmppCallEvent, timestamp time.Time) string {
	if session.WasAnswered {
		if duration := callDuration(session, timestamp); duration > 0 {
			return fmt.Sprintf("Call ended after %s", formatCallDuration(duration))
		}
		return "Call ended"
	}
	switch call.Reason {
	case "busy", "decline":
		if session.InitiatedByMe {
			if call.Reason == "busy" {
				return "Call busy"
			}
			return "Call declined"
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
		return "Call ended"
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
	nc.callSessions[key] = updated
	return notice
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
	roomType := ptr.Ptr(database.RoomTypeDM)
	resync := &simplevent.ChatResync{
		EventMeta: simplevent.EventMeta{
			Type:         bridgev2.RemoteEventChatResync,
			PortalKey:    networkid.PortalKey{ID: portalID},
			CreatePortal: true,
			Timestamp:    timestamp,
		},
		ChatInfo: &bridgev2.ChatInfo{
			Name: &bareFromJid,
			Type: roomType,
		},
	}
	nc.bridge.QueueRemoteEvent(nc.login, resync)
	msgID := nc.extractMessageID(msg)
	if msgID == "" {
		msgID = fmt.Sprintf("call-%s-%s", call.Action, call.SessionID)
	}
	nc.bridge.QueueRemoteEvent(nc.login, &simplevent.Message[string]{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventMessage,
			PortalKey: networkid.PortalKey{ID: portalID},
			Sender: bridgev2.EventSender{
				Sender:   MakeUserID(senderID),
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
