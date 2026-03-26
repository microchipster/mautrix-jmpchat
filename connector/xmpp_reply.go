package connector

import (
	"context"
	"strings"
	"unicode/utf8"

	"gosrc.io/xmpp/stanza"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"
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

func (nc *MyNetworkClient) buildOutgoingXMPPReplyQuote(ctx context.Context, portal *bridgev2.Portal, target *database.Message) string {
	if portal == nil || target == nil || portal.MXID == "" || target.MXID == "" {
		return ""
	}
	botIntent := nc.connector.bridge.Matrix.BotIntent()
	evt, err := botIntent.GetEvent(ctx, portal.MXID, target.MXID)
	if err != nil || evt == nil {
		return ""
	}
	content := evt.Content.AsMessage()
	if content == nil {
		return ""
	}
	quoteText := extractMatrixReplyQuoteText(content)
	if quoteText == "" {
		return ""
	}
	return quoteXMPPReplyText(limitReplyQuoteLines(quoteText, 2)) + "\n\n"
}

func extractMatrixReplyQuoteText(content *event.MessageEventContent) string {
	if content == nil {
		return ""
	}
	quoteText := strings.TrimSpace(content.GetCaption())
	if quoteText == "" {
		quoteText = strings.TrimSpace(content.Body)
	}
	if quoteText == "" {
		quoteText = strings.TrimSpace(content.GetFileName())
	}
	return trimBlankLines(quoteText)
}

func limitReplyQuoteLines(text string, maxLines int) string {
	text = trimBlankLines(text)
	if text == "" || maxLines <= 0 {
		return text
	}
	lines := strings.Split(text, "\n")
	trimmed := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		trimmed = append(trimmed, line)
		if len(trimmed) >= maxLines {
			break
		}
	}
	return strings.Join(trimmed, "\n")
}

func quoteXMPPReplyText(text string) string {
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, ">") {
			lines[i] = ">" + line
		} else {
			lines[i] = "> " + line
		}
	}
	return strings.Join(lines, "\n")
}

func addReplyFallbackToBody(body string, extensions []stanza.MsgExtension, quoted string) (string, []stanza.MsgExtension) {
	quoted = strings.TrimSpace(quoted)
	if quoted == "" {
		return body, extensions
	}
	quoted += "\n\n"
	shift := utf8.RuneCountInString(quoted)
	shifted := make([]stanza.MsgExtension, 0, len(extensions)+1)
	for _, ext := range extensions {
		switch typed := ext.(type) {
		case *XMPPFallback:
			copied := *typed
			copied.Body = shiftFallbackBodies(copied.Body, shift)
			shifted = append(shifted, &copied)
		case XMPPFallback:
			typed.Body = shiftFallbackBodies(typed.Body, shift)
			shifted = append(shifted, typed)
		default:
			shifted = append(shifted, ext)
		}
	}
	shifted = append(shifted, &XMPPFallback{
		For:  NSReply,
		Body: []XMPPFallbackBody{{Start: 0, End: shift}},
	})
	return quoted + body, shifted
}

func shiftFallbackBodies(bodies []XMPPFallbackBody, delta int) []XMPPFallbackBody {
	if len(bodies) == 0 || delta == 0 {
		return bodies
	}
	shifted := make([]XMPPFallbackBody, len(bodies))
	for i, body := range bodies {
		shifted[i] = XMPPFallbackBody{Start: body.Start + delta, End: body.End + delta}
	}
	return shifted
}
