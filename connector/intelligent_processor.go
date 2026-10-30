// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/id"
)

// keywordTopics maps words people type in the management room to the guide that answers them. Any
// token of a message can trigger it, so "how do I set up calls?" finds the calling guide.
var keywordTopics = map[string]string{
	// calling
	"call": "calling", "calls": "calling", "calling": "calling", "sip": "calling",
	"phone": "calling", "phones": "calling", "voice": "calling", "telephone": "calling",
	"dial": "calling", "dialing": "calling",
	// account
	"account": "account", "accounts": "account", "jmp": "account", "cheogram": "account",
	"billing": "account", "balance": "account", "usage": "account", "transactions": "account",
	"topup": "account", "top-up": "account", "plan": "account", "voicemail": "account",
	"ogm": "account", "renewal": "account", "payment": "account",
	// texting
	"sms": "sms", "mms": "sms", "text": "sms", "texts": "sms", "texting": "sms", "messaging": "sms",
	// encryption
	"encryption": "encryption", "e2ee": "encryption", "encrypted": "encryption", "encrypt": "encryption",
	// chats
	"chat": "chats", "chats": "chats", "contacts": "chats", "roster": "chats",
	"group": "chats", "groups": "chats",
	// troubleshooting
	"troubleshoot": "troubleshooting", "troubleshooting": "troubleshooting", "broken": "troubleshooting",
	"problem": "troubleshooting", "stuck": "troubleshooting", "error": "troubleshooting",
	"errors": "troubleshooting", "failing": "troubleshooting", "failed": "troubleshooting",
	"debug": "troubleshooting",
	// docs
	"docs": "docs", "documentation": "docs", "guide": "docs", "guides": "docs",
	"topics": "docs", "commands": "docs",
}

// intelligentProcessor wraps the bridge's command processor for the management room:
// unrecognised messages get the matching topic guide or a "did you mean" suggestion instead of the
// stock unknown-command reply. Everything that looks like a real command (or arrives in any other
// room) is delegated to the wrapped processor unchanged.
type intelligentProcessor struct {
	br    *bridgev2.Bridge
	proc  *commands.Processor
	known map[string]struct{}
}

var _ bridgev2.CommandProcessor = (*intelligentProcessor)(nil)

// installIntelligentProcessor replaces br.Commands with the wrapper. Call it only after every
// command handler has been registered (i.e. at the end of PostInitExtensions), or the catalog will
// be incomplete and real commands would be answered by the guides instead.
func (c *MyConnector) installIntelligentProcessor(br *bridgev2.Bridge, proc *commands.Processor) {
	if br == nil || proc == nil {
		return
	}
	if _, ok := br.Commands.(*intelligentProcessor); ok {
		return
	}
	br.Commands = &intelligentProcessor{br: br, proc: proc, known: commandCatalog(proc)}
}

// Handle implements bridgev2.CommandProcessor.
func (p *intelligentProcessor) Handle(ctx context.Context, roomID id.RoomID, eventID id.EventID, user *bridgev2.User, message string, replyTo id.EventID) {
	if !p.shouldIntercept(user, roomID, message) {
		p.proc.Handle(ctx, roomID, eventID, user, message, replyTo)
		return
	}
	ce := &commands.Event{
		Bot:        p.br.Bot,
		Bridge:     p.br,
		Processor:  p.proc,
		RoomID:     roomID,
		OrigRoomID: roomID,
		EventID:    eventID,
		User:       user,
		Ctx:        ctx,
		Log:        &p.br.Log,
		ReplyTo:    replyTo,
	}
	ce.Reply("%s", p.response(message))
}

// shouldIntercept reports whether the guides should answer the message: it must be in the user's
// management room, must not be part of an interactive prompt (sip-login and friends read arbitrary
// text), must not start with a known command or alias, and the catalog must be available.
func (p *intelligentProcessor) shouldIntercept(user *bridgev2.User, roomID id.RoomID, message string) bool {
	if p == nil || user == nil || user.ManagementRoom == "" || roomID != user.ManagementRoom {
		return false
	}
	if len(p.known) == 0 {
		return false
	}
	if state := commands.LoadCommandState(user); state != nil && state.Next != nil {
		return false
	}
	fields := strings.Fields(message)
	if len(fields) == 0 {
		return true
	}
	_, isCommand := p.known[strings.ToLower(cleanToken(fields[0]))]
	return !isCommand
}

// response builds the guide reply for a message the guides answer: the first keyword wins, a single
// near-miss word gets a suggestion, and everything else gets the topic overview.
func (p *intelligentProcessor) response(message string) string {
	tokens := strings.Fields(message)
	for i, raw := range tokens {
		token := strings.ToLower(cleanToken(raw))
		if token == "" {
			continue
		}
		if topic, ok := keywordTopics[token]; ok {
			if guide, ok := topicGuide(topic); ok {
				return guide
			}
		}
		if i == 0 && len(tokens) == 1 && len(token) >= 3 {
			if suggestion, ok := closestCommand(token, p.known); ok {
				return fmt.Sprintf("`%s` is not a command. Did you mean `%s`?\n\n%s", token, suggestion, p.hint())
			}
		}
	}
	return p.hint()
}

// hint is the friendly catch-all reply.
func (p *intelligentProcessor) hint() string {
	prefix := ""
	if p.br != nil && p.br.Config != nil {
		prefix = p.br.Config.CommandPrefix
	}
	return fmt.Sprintf("I did not recognise that as a command or a topic I have a guide for.\n\nGuides: %s.\n\nSend `%shelp` for the command list, or `%shelp <topic>` for one of the guides.", quotedTopics(), prefix, prefix)
}

// commandCatalog lists every registered command name and alias. The command processor has no
// accessor for its handlers, so reach the unexported maps by reflection; an empty result makes the
// wrapper delegate everything, so the feature degrades to the stock behavior instead of breaking.
func commandCatalog(proc *commands.Processor) map[string]struct{} {
	known := make(map[string]struct{})
	if proc == nil {
		return known
	}
	v := reflect.ValueOf(proc).Elem()
	for _, fieldName := range []string{"handlers", "aliases"} {
		field := v.FieldByName(fieldName)
		if !field.IsValid() || field.Kind() != reflect.Map {
			continue
		}
		iter := field.MapRange()
		for iter.Next() {
			known[strings.ToLower(iter.Key().String())] = struct{}{}
		}
	}
	return known
}

// closestCommand finds the nearest known command or alias within two edits. Short tokens are not
// suggested for.
func closestCommand(token string, known map[string]struct{}) (string, bool) {
	if len(token) < 3 {
		return "", false
	}
	best, bestDist := "", 3
	for name := range known {
		if absInt(len(name)-len(token)) >= bestDist {
			continue
		}
		dist := levenshtein(token, name)
		if dist < bestDist || (dist == bestDist && name < best) {
			best, bestDist = name, dist
		}
	}
	if bestDist <= 2 {
		return best, true
	}
	return "", false
}

// cleanToken strips the punctuation people naturally type around words.
func cleanToken(token string) string {
	return strings.Trim(token, ".,!?;:()[]{}<>\"'`")
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// levenshtein is the plain edit distance; commands are short ASCII, so a simple DP is fine.
func levenshtein(a, b string) int {
	if a == b {
		return 0
	}
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}
