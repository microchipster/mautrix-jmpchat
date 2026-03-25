package connector

import (
	"testing"

	"gosrc.io/xmpp/stanza"
	"maunium.net/go/mautrix/bridgev2"
)

func TestExtractXMPPReceiptTarget(t *testing.T) {
	tests := []struct {
		name       string
		msg        stanza.Message
		wantType   bridgev2.RemoteEventType
		wantTarget string
		wantOK     bool
	}{
		{
			name: "delivery receipt",
			msg: stanza.Message{Extensions: []stanza.MsgExtension{
				&XMPPReceiptReceived{ID: "msg-1"},
			}},
			wantType:   bridgev2.RemoteEventDeliveryReceipt,
			wantTarget: "msg-1",
			wantOK:     true,
		},
		{
			name: "displayed marker",
			msg: stanza.Message{Extensions: []stanza.MsgExtension{
				&XMPPMarkerDisplayed{ID: "msg-2"},
			}},
			wantType:   bridgev2.RemoteEventReadReceipt,
			wantTarget: "msg-2",
			wantOK:     true,
		},
		{
			name: "missing id",
			msg: stanza.Message{Extensions: []stanza.MsgExtension{
				&XMPPMarkerReceived{},
			}},
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotType, gotTarget, gotOK := extractXMPPReceiptTarget(tt.msg)
			if gotOK != tt.wantOK {
				t.Fatalf("ok = %v, want %v", gotOK, tt.wantOK)
			}
			if gotType != tt.wantType {
				t.Fatalf("type = %v, want %v", gotType, tt.wantType)
			}
			if gotTarget != tt.wantTarget {
				t.Fatalf("target = %q, want %q", gotTarget, tt.wantTarget)
			}
		})
	}
}
