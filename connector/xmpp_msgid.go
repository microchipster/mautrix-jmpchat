// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/google/uuid"
	"gosrc.io/xmpp/stanza"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

// Message identity for inbound XMPP stanzas:
//
//  1. the `id` attribute (kept for compatibility with rows already bridged
//     and with client-chosen ids);
//  2. a trusted XEP-0359 stanza-id scoped by its assigner (`xmp:sid:<scope>:<id>`):
//     a sender reusing origin-ids can then never make existing rows swallow
//     distinct messages;
//  3. the XEP-0359 origin-id;
//  4. for delayed (history) stanzas without any id: a hash of the trusted
//     delay plus the sender, scope, body and attachment URL, so repeated
//     history replays of the same stanza collapse into one row;
//  5. a random id as the last resort (a replayed id-less stanza without delay
//     has no better identity available).
const (
	msgIDSidPrefix  = "xmp:sid:"
	msgIDHashPrefix = "xmp:h:"
	msgIDRandPrefix = "xmp:r:"
	// dupWindow bounds the cross-path duplicate lookup.
	dupWindow = 30 * 24 * time.Hour
)

// minDelayStamp ignores XEP-0203 stamps that are obviously bogus.
var minDelayStamp = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// inboundIDs carries the identity evidence of one inbound stanza.
type inboundIDs struct {
	idAttr     string // `id` attribute (or the MAM archive id for archived stanzas without one)
	originID   string
	stanzaID   string // trusted only
	scope      string // who assigned the stanza-id: our bare JID, or the room for groupchat
	delay      time.Time
	senderFull string // as received (occupant JID for groupchat)
	body       string // raw body and attachment URL, before any rewriting
	oob        string
}

// inboundMessageID computes the bridgev2 message id for an inbound stanza.
func inboundMessageID(ids inboundIDs) networkid.MessageID {
	if ids.idAttr != "" {
		return networkid.MessageID(ids.idAttr)
	}
	if ids.stanzaID != "" && ids.scope != "" {
		return networkid.MessageID(msgIDSidPrefix + strings.ToLower(ids.scope) + ":" + ids.stanzaID)
	}
	if ids.originID != "" {
		return networkid.MessageID(ids.originID)
	}
	if !ids.delay.IsZero() {
		h := sha256.New()
		for _, part := range []string{
			strings.ToLower(ids.scope),
			strings.ToLower(ids.senderFull),
			ids.delay.UTC().Format(time.RFC3339Nano),
			ids.body,
			ids.oob,
		} {
			h.Write([]byte(part))
			h.Write([]byte{0})
		}
		return networkid.MessageID(msgIDHashPrefix + hex.EncodeToString(h.Sum(nil))[:32])
	}
	return networkid.MessageID(msgIDRandPrefix + uuid.NewString())
}

// ownScope is the stanza-id assigner for stanzas addressed to this login.
func (nc *MyNetworkClient) ownScope() string {
	jid := nc.ownBareJID()
	if jid == "" && nc.login != nil {
		jid = string(nc.login.ID)
	}
	return strings.ToLower(bareJID(UnescapeJID(jid)))
}

// delayDomain is the bare domain of a JID (the whole bare JID when there is no
// domain part).
func delayDomain(jid string) string {
	j := bareJID(strings.TrimSpace(jid))
	if i := strings.LastIndexByte(j, '@'); i >= 0 {
		return strings.ToLower(j[i+1:])
	}
	return strings.ToLower(j)
}

// mucDelayTrusted reports whether a XEP-0203 delay in a MUC context is
// trusted: only delays stamped by the room itself are, so a participant cannot
// fake a message timestamp.
func (nc *MyNetworkClient) mucDelayTrusted(roomBare, from string) bool {
	return from != "" && strings.EqualFold(bareJID(from), bareJID(roomBare))
}

// dmDelayTrusted reports whether a XEP-0203 delay in a 1:1 context is trusted:
// delays without a `from`, or stamped by our own domain or the sender's
// domain, are.
func (nc *MyNetworkClient) dmDelayTrusted(senderBare, from string) bool {
	if strings.TrimSpace(from) == "" {
		return true
	}
	fd := delayDomain(from)
	return fd == delayDomain(nc.ownBareJID()) || fd == delayDomain(senderBare)
}

// trustedDelay returns the (clamped) XEP-0203 stamp of a stanza when it is
// trusted in context, and the zero time otherwise.
func (nc *MyNetworkClient) trustedDelay(msg stanza.Message, roomBare, senderBare string, isMUC bool, now time.Time) time.Time {
	stamp := ""
	var delay XMPPDelay
	if msg.Get(&delay) {
		stamp = delay.Stamp
		trusted := false
		if isMUC {
			trusted = nc.mucDelayTrusted(roomBare, delay.From)
		} else {
			trusted = nc.dmDelayTrusted(senderBare, delay.From)
		}
		if !trusted {
			return time.Time{}
		}
	} else {
		var delayOld XMPPDelayOld
		if !msg.Get(&delayOld) {
			return time.Time{}
		}
		// Legacy jabber:x:delay has no `from`; only 1:1 contexts trust it.
		if isMUC {
			return time.Time{}
		}
		stamp = delayOld.InnerXML
	}
	stamp = strings.TrimSpace(stamp)
	if stamp == "" {
		return time.Time{}
	}
	var parsed time.Time
	ok := false
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "20060102T15:04:05"} {
		if t, err := time.Parse(layout, stamp); err == nil {
			parsed, ok = t, true
			break
		}
	}
	if !ok || parsed.Before(minDelayStamp) {
		return time.Time{}
	}
	if parsed.After(now) {
		parsed = now
	}
	return parsed
}

// dupCandidate is one inbound message checked against the message rows already
// in the database (cross-path dedupe: the same stanza can arrive live, as a
// carbon and through MAM).
type dupCandidate struct {
	id       networkid.MessageID
	idAttr   string
	originID string
	stanzaID string
	sender   networkid.UserID
	fromMe   bool
	ts       time.Time
}

// effectivePortalKey builds the portal key used by alias lookups for a portal
// ID: the exact key the database stores (split portals use the login ID as the
// receiver, non-split portals have an empty receiver).
func (nc *MyNetworkClient) effectivePortalKey(portalID networkid.PortalID) networkid.PortalKey {
	key := networkid.PortalKey{ID: portalID}
	if nc.bridge == nil || nc.bridge.Config == nil || nc.login == nil {
		return key
	}
	if !nc.bridge.Config.SplitPortals {
		if cached := nc.bridge.FindCachedPortalReceiver(portalID, nc.login.ID); !cached.IsEmpty() {
			return cached
		}
		return key
	}
	return networkid.PortalKey{ID: portalID, Receiver: nc.login.ID}
}

// isBridgedDuplicate returns the id of an already-bridged row that carries
// this message under a different id, or "". Lookup failures log a warning and
// report "not a duplicate" so a duplicate is preferred over a lost message.
func (nc *MyNetworkClient) isBridgedDuplicate(ctx context.Context, portalKey networkid.PortalKey, c dupCandidate) networkid.MessageID {
	id, err := nc.bridgedDuplicate(ctx, portalKey, c)
	if err != nil {
		nc.log.Warn().Err(err).Str("id", string(c.id)).Msg("Failed to check for cross-path duplicate message")
		return ""
	}
	return id
}

// bridgedDuplicate looks for an existing row of the same message under other
// identifiers, scoped to one portal and to a bounded window ending at the
// message timestamp.
//
// A trusted stanza-id is checked for any sender (a stanza-id names exactly one
// message; a replay of our own message under an earlier nickname still
// counts). Sender-chosen evidence (origin-id, id attribute) is only used for
// our own messages, so a contact reusing an id can never make us drop their
// messages.
func (nc *MyNetworkClient) bridgedDuplicate(ctx context.Context, portalKey networkid.PortalKey, c dupCandidate) (networkid.MessageID, error) {
	if nc.bridge == nil || nc.bridge.DB == nil || portalKey.Receiver == "" {
		return "", nil
	}
	since := time.Time{}
	if !c.ts.IsZero() {
		since = c.ts.Add(-dupWindow)
	}
	match := func(ids []networkid.MessageID, trustedID bool) (networkid.MessageID, error) {
		for _, id := range ids {
			if id == c.id {
				continue
			}
			ok, err := nc.dupRowMatches(ctx, portalKey, id, c, trustedID)
			if err != nil {
				return "", err
			}
			if ok {
				return id, nil
			}
		}
		return "", nil
	}
	// 1. Trusted stanza-id (any sender).
	if c.stanzaID != "" {
		ids, err := nc.aliasMessageIDsSince(ctx, portalKey, []string{"stanza_id"}, c.stanzaID, since)
		if err != nil {
			return "", err
		}
		if id, err := match(ids, true); err != nil || id != "" {
			return id, err
		}
	}
	if !c.fromMe {
		return "", nil
	}
	// 2. Our own message: origin-id / id attribute can be matched against the
	// metadata of Matrix-originated rows too.
	if c.originID != "" {
		ids, err := nc.aliasMessageIDsSince(ctx, portalKey, []string{"origin_id", "xmpp_message_id"}, c.originID, since)
		if err != nil {
			return "", err
		}
		if id, err := match(ids, false); err != nil || id != "" {
			return id, err
		}
		if networkid.MessageID(c.originID) != c.id {
			ok, err := nc.dupRowMatches(ctx, portalKey, networkid.MessageID(c.originID), c, false)
			if err != nil {
				return "", err
			}
			if ok {
				return networkid.MessageID(c.originID), nil
			}
		}
	}
	if c.idAttr != "" && c.idAttr != c.originID {
		ids, err := nc.aliasMessageIDsSince(ctx, portalKey, []string{"origin_id", "xmpp_message_id"}, c.idAttr, since)
		if err != nil {
			return "", err
		}
		if id, err := match(ids, false); err != nil || id != "" {
			return id, err
		}
	}
	return "", nil
}

// dupRowMatches reports whether the message row id holds this candidate: the
// row must be in the same portal and come from the same sender (or, for a
// trusted stanza-id, from our own login: a replay under an earlier nick still
// names the same message).
func (nc *MyNetworkClient) dupRowMatches(ctx context.Context, portalKey networkid.PortalKey, id networkid.MessageID, c dupCandidate, trustedID bool) (bool, error) {
	if nc.bridge == nil || nc.bridge.DB == nil || id == "" {
		return false, nil
	}
	parts, err := nc.bridge.DB.Message.GetAllPartsByID(ctx, portalKey.Receiver, id)
	if err != nil {
		return false, err
	}
	for _, part := range parts {
		if part.Room != portalKey {
			continue
		}
		if nc.dupSameSender(part, c) {
			return true, nil
		}
		if trustedID && c.fromMe && nc.rowIsOwnSender(part.SenderID) {
			return true, nil
		}
	}
	return false, nil
}

// dupSameSender reports whether a stored row has the same sender as the
// candidate.
func (nc *MyNetworkClient) dupSameSender(part *database.Message, c dupCandidate) bool {
	rowSender := CanonicalBareJID(UnescapeJID(string(part.SenderID)))
	candSender := CanonicalBareJID(UnescapeJID(string(c.sender)))
	if rowSender != "" && strings.EqualFold(rowSender, candSender) {
		return true
	}
	return c.fromMe && nc.rowIsOwnSender(part.SenderID)
}

// rowIsOwnSender reports whether a stored row was sent by this login.
func (nc *MyNetworkClient) rowIsOwnSender(sender networkid.UserID) bool {
	own := nc.ownBareJID()
	if own == "" {
		return false
	}
	return strings.EqualFold(CanonicalBareJID(UnescapeJID(string(sender))), CanonicalBareJID(own))
}
