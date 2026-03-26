package connector

import (
	"strings"

	"gosrc.io/xmpp/stanza"
	"maunium.net/go/mautrix/bridgev2/database"
)

func extractXMPPReplyTarget(msg stanza.Message) string {
	var reply XMPPReply
	if !msg.Get(&reply) {
		return ""
	}
	return strings.TrimSpace(reply.ID)
}

func buildOutgoingXMPPReply(target *database.Message) *XMPPReply {
	if target == nil || target.ID == "" {
		return nil
	}
	reply := &XMPPReply{ID: string(target.ID)}
	if target.SenderID != "" {
		reply.To = string(target.SenderID)
	}
	return reply
}
