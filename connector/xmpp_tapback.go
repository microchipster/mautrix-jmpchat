// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"regexp"
	"strings"
	"time"

	"gosrc.io/xmpp/stanza"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
)

// iPhone (iMessage) tapbacks reach an SMS bridge as ordinary text messages:
//
//	Liked “see you at 8”
//	Loved an image
//	Removed a like from “see you at 8”
//
// Quoted text uses curly quotes, but some carriers/gateways rewrite them to
// straight quotes, so both are accepted. This file recognizes those texts and
// converts them into Matrix reactions, falling back to a plain text message
// when the referenced message cannot be found.

// tapbackAddedEmoji maps the tapback name used when a tapback is added.
var tapbackAddedEmoji = map[string]string{
	"loved":      "❤️",
	"liked":      "👍",
	"disliked":   "👎",
	"laughed at": "😂",
	"emphasized": "‼️",
	"questioned": "❓",
}

// tapbackRemovedEmoji maps the noun used when a tapback is removed.
var tapbackRemovedEmoji = map[string]string{
	"love":     "❤️",
	"heart":    "❤️",
	"like":     "👍",
	"dislike":  "👎",
	"laugh":    "😂",
	"emphasis": "‼️",
	"question": "❓",
}

var (
	tapbackAddedTextRegex    = regexp.MustCompile(`^(Loved|Liked|Disliked|Laughed at|Emphasized|Questioned) [“"](.*)[”"]$`)
	tapbackAddedMediaRegex   = regexp.MustCompile(`^(Loved|Liked|Disliked|Laughed at|Emphasized|Questioned) (?:an? [A-Za-z ]+)$`)
	tapbackRemovedTextRegex  = regexp.MustCompile(`^Removed (?:a|an) (like|love|heart|dislike|laugh|emphasis|question) from [“"](.*)[”"]$`)
	tapbackRemovedMediaRegex = regexp.MustCompile(`^Removed (?:a|an) (like|love|heart|dislike|laugh|emphasis|question) from an? [A-Za-z ]+$`)
)

// tapback is a parsed iMessage tapback.
type tapback struct {
	// Emoji is the Matrix reaction emoji the tapback maps to.
	Emoji string
	// TargetText is the quoted original message body. Empty for media tapbacks.
	TargetText string
	// IsMedia marks tapbacks that refer to an attachment ("Liked an image").
	IsMedia bool
	// IsRemoval marks tapbacks that remove a previously added tapback.
	IsRemoval bool
}

// parseTapback recognizes an iMessage tapback in a message body.
// It returns false for any body that is not a tapback.
func parseTapback(body string) (tapback, bool) {
	body = strings.TrimSpace(body)
	if body == "" {
		return tapback{}, false
	}

	if match := tapbackRemovedTextRegex.FindStringSubmatch(body); match != nil {
		if emoji, ok := tapbackRemovedEmoji[strings.ToLower(match[1])]; ok {
			return tapback{Emoji: emoji, TargetText: match[2], IsRemoval: true}, true
		}
		return tapback{}, false
	}
	if match := tapbackRemovedMediaRegex.FindStringSubmatch(body); match != nil {
		if emoji, ok := tapbackRemovedEmoji[strings.ToLower(match[1])]; ok {
			return tapback{Emoji: emoji, IsMedia: true, IsRemoval: true}, true
		}
		return tapback{}, false
	}
	if match := tapbackAddedTextRegex.FindStringSubmatch(body); match != nil {
		if emoji, ok := tapbackAddedEmoji[strings.ToLower(match[1])]; ok {
			return tapback{Emoji: emoji, TargetText: match[2]}, true
		}
		return tapback{}, false
	}
	if match := tapbackAddedMediaRegex.FindStringSubmatch(body); match != nil {
		if emoji, ok := tapbackAddedEmoji[strings.ToLower(match[1])]; ok {
			return tapback{Emoji: emoji, IsMedia: true}, true
		}
		return tapback{}, false
	}
	return tapback{}, false
}

// tapbackLookback is how many recent portal messages are scanned when looking
// for the message a tapback refers to.
const tapbackLookback = 100

// matchTapbackTarget finds the most recent message a tapback refers to.
// Messages are expected newest-first, as returned by GetLastNInPortal.
//
// Text tapbacks quote the original body, but SMS gateways may truncate long
// messages, so an exact match is preferred and a prefix match is accepted as a
// fallback. Media tapbacks ("Liked an image") match the newest attachment.
func matchTapbackTarget(messages []*database.Message, tb tapback) *database.Message {
	quoted := normalizeTapbackQuotedText(tb.TargetText)
	for _, msg := range messages {
		if msg == nil {
			continue
		}
		meta, ok := msg.Metadata.(*MessageMetadata)
		if !ok || meta == nil {
			continue
		}
		if tb.IsMedia {
			if meta.HasAttachment {
				return msg
			}
			continue
		}
		body := strings.TrimSpace(meta.Body)
		if body == "" || quoted == "" {
			continue
		}
		if body == quoted || strings.HasPrefix(body, quoted) {
			return msg
		}
	}
	return nil
}

// normalizeTapbackQuotedText strips trailing ellipses that gateways add when
// they truncate the quoted message.
func normalizeTapbackQuotedText(text string) string {
	text = strings.TrimSpace(text)
	text = strings.TrimSuffix(text, "…")
	text = strings.TrimSuffix(text, "...")
	return strings.TrimSpace(text)
}

// handleIncomingXMPPTapback converts a tapback into a Matrix reaction.
// It reports whether the tapback was handled; when the target message is not
// found the caller should forward the original text instead, so no information
// is lost.
func (nc *MyNetworkClient) handleIncomingXMPPTapback(msg stanza.Message, timestamp time.Time, bareFromJid, senderID string, isFromMe bool, tb tapback) bool {
	if nc.bridge == nil || nc.bridge.DB == nil {
		return false
	}

	portalKey := networkid.PortalKey{ID: MakePortalID(bareFromJid)}
	messages, err := nc.bridge.DB.Message.GetLastNInPortal(context.Background(), portalKey, tapbackLookback)
	if err != nil {
		nc.log.Warn().Err(err).Msg("Failed to load recent messages for tapback matching")
		return false
	}

	target := matchTapbackTarget(messages, tb)
	if target == nil {
		nc.log.Debug().
			Str("from", msg.From).
			Bool("media", tb.IsMedia).
			Bool("removal", tb.IsRemoval).
			Str("target_text", tb.TargetText).
			Msg("Tapback target not found; forwarding as a regular message")
		return false
	}

	sender := bridgev2.EventSender{
		Sender:   MakeUserID(senderID),
		IsFromMe: isFromMe,
	}

	// Keep the same portal-resync behavior as regular XMPP reactions, so the
	// portal exists and has up-to-date metadata even if this is the first event.
	nc.bridge.QueueRemoteEvent(nc.login, &simplevent.ChatResync{
		EventMeta: simplevent.EventMeta{
			Type:         bridgev2.RemoteEventChatResync,
			PortalKey:    portalKey,
			CreatePortal: true,
			Timestamp:    timestamp,
		},
		ChatInfo: nil,
	})

	reactionItems := make([]*bridgev2.BackfillReaction, 0, 1)
	if !tb.IsRemoval {
		reactionItems = append(reactionItems, &bridgev2.BackfillReaction{
			Timestamp: timestamp,
			Sender:    sender,
			EmojiID:   networkid.EmojiID(tb.Emoji),
			Emoji:     tb.Emoji,
		})
	}

	// HasAllReactions makes this an authoritative snapshot for the sender:
	// an added tapback replaces any previous one, a removal clears it.
	nc.bridge.QueueRemoteEvent(nc.login, &simplevent.ReactionSync{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventReactionSync,
			PortalKey: portalKey,
			Sender:    sender,
			Timestamp: timestamp,
		},
		TargetMessage: target.ID,
		Reactions: &bridgev2.ReactionSyncData{
			Users: map[networkid.UserID]*bridgev2.ReactionSyncUser{
				MakeUserID(senderID): {
					Reactions:       reactionItems,
					HasAllReactions: true,
				},
			},
		},
	})

	nc.log.Info().
		Str("from", msg.From).
		Str("sender_id", senderID).
		Str("target_message_id", string(target.ID)).
		Str("emoji", tb.Emoji).
		Bool("removal", tb.IsRemoval).
		Msg("Converted tapback into Matrix reaction")
	return true
}
