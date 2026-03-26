package connector

import (
	"testing"

	"gosrc.io/xmpp/stanza"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
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

func TestQuoteXMPPReplyText(t *testing.T) {
	quoted := quoteXMPPReplyText("first line\n> nested")
	if quoted != "> first line\n>> nested" {
		t.Fatalf("unexpected quoted text: %q", quoted)
	}
}

func TestAddReplyFallbackToBodyShiftsExistingFallbacks(t *testing.T) {
	body, extensions := addReplyFallbackToBody("Caption\nhttps://example.com/file.jpg", []stanza.MsgExtension{
		&XMPPFallback{For: "jabber:x:oob", Body: []XMPPFallbackBody{{Start: 8, End: 36}}},
	}, "> original")
	if body != "> original\n\nCaption\nhttps://example.com/file.jpg" {
		t.Fatalf("unexpected body: %q", body)
	}
	var replyFallback, oobFallback *XMPPFallback
	for _, ext := range extensions {
		switch typed := ext.(type) {
		case *XMPPFallback:
			if typed.For == NSReply {
				replyFallback = typed
			} else if typed.For == "jabber:x:oob" {
				oobFallback = typed
			}
		}
	}
	if replyFallback == nil || len(replyFallback.Body) != 1 || replyFallback.Body[0].Start != 0 || replyFallback.Body[0].End != 12 {
		t.Fatalf("unexpected reply fallback: %#v", replyFallback)
	}
	if oobFallback == nil || len(oobFallback.Body) != 1 || oobFallback.Body[0].Start != 20 || oobFallback.Body[0].End != 48 {
		t.Fatalf("unexpected shifted oob fallback: %#v", oobFallback)
	}
}

func TestExtractMatrixReplyQuoteText(t *testing.T) {
	content := &event.MessageEventContent{Body: "caption", FileName: "file.jpg"}
	if got := extractMatrixReplyQuoteText(content); got != "caption" {
		t.Fatalf("unexpected reply quote text: %q", got)
	}
	content = &event.MessageEventContent{Body: "file.jpg", FileName: "file.jpg"}
	if got := extractMatrixReplyQuoteText(content); got != "file.jpg" {
		t.Fatalf("unexpected file-name fallback quote text: %q", got)
	}
}
