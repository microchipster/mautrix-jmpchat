package connector

import (
	"testing"

	"gosrc.io/xmpp/stanza"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

func TestExtractXMPPReplyTarget(t *testing.T) {
	msg := stanza.Message{
		Extensions: []stanza.MsgExtension{
			&XMPPReply{ID: "remote-123", To: "+15551234567@cheogram.com"},
		},
	}

	if target := extractXMPPReplyTarget(msg); target != "remote-123" {
		t.Fatalf("unexpected reply target: %q", target)
	}
}

func TestBuildOutgoingXMPPReply(t *testing.T) {
	reply := buildOutgoingXMPPReply(&database.Message{
		ID:       networkid.MessageID("remote-456"),
		SenderID: networkid.UserID("+15557654321@cheogram.com"),
	})
	if reply == nil {
		t.Fatal("expected outgoing reply")
	}
	if reply.ID != "remote-456" {
		t.Fatalf("unexpected reply id: %q", reply.ID)
	}
	if reply.To != "+15557654321@cheogram.com" {
		t.Fatalf("unexpected reply to: %q", reply.To)
	}
}
