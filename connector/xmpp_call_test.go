package connector

import (
	"encoding/xml"
	"testing"
	"time"

	"gosrc.io/xmpp/stanza"
)

func TestExtractXMPPCallEventPropose(t *testing.T) {
	msg := stanza.Message{Extensions: []stanza.MsgExtension{&XMPPJinglePropose{
		ID:           "sess-1",
		Descriptions: []XMPPJingleDescription{{XMLName: xml.Name{Space: NSJingleRTP, Local: "description"}}},
	}}}
	call := extractXMPPCallEvent(msg)
	if call == nil || call.Action != "propose" || call.SessionID != "sess-1" {
		t.Fatalf("unexpected call event: %#v", call)
	}
}

func TestExtractXMPPCallEventIgnoresNonRTPPropose(t *testing.T) {
	msg := stanza.Message{Extensions: []stanza.MsgExtension{&XMPPJinglePropose{
		ID:           "sess-2",
		Descriptions: []XMPPJingleDescription{{XMLName: xml.Name{Space: "urn:xmpp:jingle:apps:file-transfer:5", Local: "description"}}},
	}}}
	if call := extractXMPPCallEvent(msg); call != nil {
		t.Fatalf("expected nil call event, got %#v", call)
	}
}

func TestExtractXMPPCallEventFinishReason(t *testing.T) {
	msg := stanza.Message{Extensions: []stanza.MsgExtension{&XMPPJingleFinish{
		ID:     "sess-3",
		Reason: &XMPPJingleReason{Condition: &XMPPJingleReasonCondition{XMLName: xml.Name{Space: NSJingle, Local: "busy"}}},
	}}}
	call := extractXMPPCallEvent(msg)
	if call == nil || call.Action != "finish" || call.Reason != "busy" {
		t.Fatalf("unexpected finish event: %#v", call)
	}
}

func TestAdvanceXMPPCallSessionIncomingMissed(t *testing.T) {
	ts := time.Unix(100, 0)
	state, notice := advanceXMPPCallSession(nil, &xmppCallEvent{Action: "propose", SessionID: "sess"}, ts, false)
	if notice != "Incoming call" {
		t.Fatalf("unexpected propose notice: %q", notice)
	}
	state, notice = advanceXMPPCallSession(state, &xmppCallEvent{Action: "retract", SessionID: "sess"}, ts.Add(30*time.Second), false)
	if notice != "Incoming call cancelled" {
		t.Fatalf("unexpected retract notice: %q", notice)
	}
}

func TestAdvanceXMPPCallSessionOutgoingAnsweredAndFinishedByPeer(t *testing.T) {
	ts := time.Unix(100, 0)
	state, notice := advanceXMPPCallSession(nil, &xmppCallEvent{Action: "propose", SessionID: "sess"}, ts, true)
	if notice != "Outgoing call" {
		t.Fatalf("unexpected propose notice: %q", notice)
	}
	state, notice = advanceXMPPCallSession(state, &xmppCallEvent{Action: "ringing", SessionID: "sess"}, ts.Add(5*time.Second), false)
	if notice != "Phone ringing" {
		t.Fatalf("unexpected ringing notice: %q", notice)
	}
	state, notice = advanceXMPPCallSession(state, &xmppCallEvent{Action: "proceed", SessionID: "sess"}, ts.Add(10*time.Second), false)
	if notice != "Call answered" {
		t.Fatalf("unexpected proceed notice: %q", notice)
	}
	_, notice = advanceXMPPCallSession(state, &xmppCallEvent{Action: "finish", SessionID: "sess", Reason: "success"}, ts.Add(70*time.Second), false)
	if notice != "Call ended after 1m (peer hung up)" {
		t.Fatalf("unexpected finish notice: %q", notice)
	}
}

func TestAdvanceXMPPCallSessionOutgoingAnsweredAndFinishedByMe(t *testing.T) {
	ts := time.Unix(100, 0)
	state, _ := advanceXMPPCallSession(nil, &xmppCallEvent{Action: "propose", SessionID: "sess"}, ts, true)
	state, _ = advanceXMPPCallSession(state, &xmppCallEvent{Action: "proceed", SessionID: "sess"}, ts.Add(5*time.Second), false)
	_, notice := advanceXMPPCallSession(state, &xmppCallEvent{Action: "finish", SessionID: "sess", Reason: "success"}, ts.Add(65*time.Second), true)
	if notice != "Call ended after 1m (you hung up)" {
		t.Fatalf("unexpected finish notice: %q", notice)
	}
}

func TestAdvanceXMPPCallSessionIncomingAnsweredAndDeclinedByMe(t *testing.T) {
	ts := time.Unix(100, 0)
	state, _ := advanceXMPPCallSession(nil, &xmppCallEvent{Action: "propose", SessionID: "sess"}, ts, false)
	state, _ = advanceXMPPCallSession(state, &xmppCallEvent{Action: "proceed", SessionID: "sess"}, ts.Add(5*time.Second), false)
	_, notice := advanceXMPPCallSession(state, &xmppCallEvent{Action: "finish", SessionID: "sess", Reason: "decline"}, ts.Add(25*time.Second), true)
	if notice != "Call ended after 20s (you declined)" {
		t.Fatalf("unexpected finish notice: %q", notice)
	}
}

func TestAdvanceXMPPCallSessionIncomingRejectedByMe(t *testing.T) {
	ts := time.Unix(100, 0)
	state, _ := advanceXMPPCallSession(nil, &xmppCallEvent{Action: "propose", SessionID: "sess"}, ts, false)
	_, notice := advanceXMPPCallSession(state, &xmppCallEvent{Action: "reject", SessionID: "sess"}, ts.Add(5*time.Second), true)
	if notice != "Declined incoming call" {
		t.Fatalf("unexpected reject notice: %q", notice)
	}
}

func TestAdvanceXMPPCallSessionFinishBusy(t *testing.T) {
	ts := time.Unix(100, 0)
	state, _ := advanceXMPPCallSession(nil, &xmppCallEvent{Action: "propose", SessionID: "sess"}, ts, true)
	_, notice := advanceXMPPCallSession(state, &xmppCallEvent{Action: "finish", SessionID: "sess", Reason: "busy"}, ts.Add(10*time.Second), false)
	if notice != "Call busy" {
		t.Fatalf("unexpected busy finish notice: %q", notice)
	}
}

func TestAdvanceXMPPCallSessionFinishTimeout(t *testing.T) {
	ts := time.Unix(100, 0)
	state, _ := advanceXMPPCallSession(nil, &xmppCallEvent{Action: "propose", SessionID: "sess"}, ts, true)
	_, notice := advanceXMPPCallSession(state, &xmppCallEvent{Action: "finish", SessionID: "sess", Reason: "timeout"}, ts.Add(25*time.Second), false)
	if notice != "Call timed out" {
		t.Fatalf("unexpected timeout finish notice: %q", notice)
	}
}

func TestAdvanceXMPPCallSessionSuppressesAfterTerminal(t *testing.T) {
	ts := time.Unix(100, 0)
	state, _ := advanceXMPPCallSession(nil, &xmppCallEvent{Action: "propose", SessionID: "sess"}, ts, false)
	state, notice := advanceXMPPCallSession(state, &xmppCallEvent{Action: "retract", SessionID: "sess"}, ts.Add(5*time.Second), false)
	if notice != "Incoming call cancelled" {
		t.Fatalf("unexpected retract notice: %q", notice)
	}
	_, notice = advanceXMPPCallSession(state, &xmppCallEvent{Action: "finish", SessionID: "sess", Reason: "cancel"}, ts.Add(10*time.Second), false)
	if notice != "" {
		t.Fatalf("expected terminal call session to suppress later notice, got %q", notice)
	}
}

func TestFormatCallNoticeForParty(t *testing.T) {
	if got := formatCallNoticeForParty("Incoming call", "+15551234567"); got != "Incoming call from +15551234567" {
		t.Fatalf("unexpected incoming formatting: %q", got)
	}
	if got := formatCallNoticeForParty("Incoming call cancelled", "Alice"); got != "Alice cancelled the call" {
		t.Fatalf("unexpected cancelled formatting: %q", got)
	}
	if got := formatCallNoticeForParty("Phone ringing", "Alice"); got != "Alice is ringing" {
		t.Fatalf("unexpected ringing formatting: %q", got)
	}
	if got := formatCallNoticeForParty("Call busy", "Alice"); got != "Alice is busy" {
		t.Fatalf("unexpected busy formatting: %q", got)
	}
	if got := formatCallNoticeForParty("Call timed out", "Alice"); got != "Alice didn't answer" {
		t.Fatalf("unexpected timed-out formatting: %q", got)
	}
	if got := formatCallNoticeForParty("Call declined", "Alice"); got != "Alice declined the call" {
		t.Fatalf("unexpected declined formatting: %q", got)
	}
	if got := formatCallNoticeForParty("Declined incoming call", "Alice"); got != "Declined call from Alice" {
		t.Fatalf("unexpected declined incoming formatting: %q", got)
	}
	if got := formatCallNoticeForParty("Call ended after 45s (peer hung up)", "Alice"); got != "Call with Alice ended after 45s (Alice hung up)" {
		t.Fatalf("unexpected ended formatting: %q", got)
	}
	if got := formatCallNoticeForParty("Call ended after 1m 24s (you hung up)", "Alice"); got != "Call with Alice ended after 1m 24s (you hung up)" {
		t.Fatalf("unexpected ended formatting: %q", got)
	}
	if got := formatCallNoticeForParty("Call ended (you hung up)", "Alice"); got != "Call with Alice ended (you hung up)" {
		t.Fatalf("unexpected ended formatting: %q", got)
	}
	if got := formatCallNoticeForParty("Call ended (peer hung up)", "Alice"); got != "Call with Alice ended (Alice hung up)" {
		t.Fatalf("unexpected ended formatting: %q", got)
	}
	if got := formatCallNoticeForParty("Call ended after 2m (connection lost)", "Alice"); got != "Call with Alice ended after 2m (connection lost)" {
		t.Fatalf("unexpected ended formatting: %q", got)
	}
	if got := formatCallNoticeForParty("Call ended after 45s (peer hung up)", ""); got != "Call ended after 45s (ended by other party)" {
		t.Fatalf("unexpected empty party formatting: %q", got)
	}
}

func TestCallPartyLabelFromJID(t *testing.T) {
	if got := callPartyLabelFromJID("+15551234567@cheogram.com"); got != "5551234567" {
		t.Fatalf("unexpected phone label: %q", got)
	}
	if got := callPartyLabelFromJID("room@example.com/Nick"); got != "Nick" {
		t.Fatalf("unexpected resource label: %q", got)
	}
}

func TestFormatCallDuration(t *testing.T) {
	if got := formatCallDuration(3661 * time.Second); got != "1h 1m 1s" {
		t.Fatalf("unexpected duration formatting: %q", got)
	}
}
