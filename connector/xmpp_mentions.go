// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"strings"

	"gosrc.io/xmpp/stanza"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// xmppMentionJIDs extracts XEP-0372 mention references from a stanza and
// returns the referenced bare JIDs, canonicalized and deduplicated.
func xmppMentionJIDs(msg stanza.Message) []string {
	var jids []string
	seen := make(map[string]struct{})
	add := func(ref XMPPReference) {
		refType := strings.TrimSpace(ref.Type)
		if refType != "" && !strings.EqualFold(refType, "mention") {
			return
		}
		uri := strings.TrimSpace(ref.URI)
		if len(uri) < 5 || !strings.EqualFold(uri[:5], "xmpp:") {
			return
		}
		jid := CanonicalBareJID(uri[5:])
		if jid == "" || strings.Contains(jid, ",") {
			return
		}
		if _, ok := seen[jid]; ok {
			return
		}
		seen[jid] = struct{}{}
		jids = append(jids, jid)
	}
	for _, ext := range msg.Extensions {
		switch ref := ext.(type) {
		case *XMPPReference:
			if ref != nil {
				add(*ref)
			}
		case XMPPReference:
			add(ref)
		}
	}
	return jids
}

// xmppMentionMXIDs converts XEP-0372 mention references into Matrix ghost
// MXIDs. It also makes sure the mentioned ghosts exist so Matrix clients can
// resolve their display names.
func (nc *MyNetworkClient) xmppMentionMXIDs(ctx context.Context, msg stanza.Message) []id.UserID {
	if nc.bridge == nil {
		return nil
	}
	var mxids []id.UserID
	seen := make(map[id.UserID]struct{})
	for _, jid := range xmppMentionJIDs(msg) {
		userID := MakeUserID(jid)
		if _, err := nc.bridge.GetGhostByID(ctx, userID); err != nil {
			continue
		}
		mxid := nc.bridge.Matrix.FormatGhostMXID(userID)
		if mxid == "" {
			continue
		}
		if _, ok := seen[mxid]; ok {
			continue
		}
		seen[mxid] = struct{}{}
		mxids = append(mxids, mxid)
	}
	return mxids
}

// xmppReferenceForJID builds a XEP-0372 mention reference for a bare JID.
func xmppReferenceForJID(jid string) (XMPPReference, bool) {
	jid = CanonicalBareJID(jid)
	if jid == "" || strings.Contains(jid, ",") {
		return XMPPReference{}, false
	}
	return XMPPReference{Type: "mention", URI: "xmpp:" + jid}, true
}

// xmppMentionReferences converts Matrix mention MXIDs into XEP-0372 mention
// references for outbound stanzas.
func (nc *MyNetworkClient) xmppMentionReferences(mentions *event.Mentions) []stanza.MsgExtension {
	if mentions == nil || len(mentions.UserIDs) == 0 || nc.bridge == nil {
		return nil
	}
	var refs []stanza.MsgExtension
	seen := make(map[string]struct{})
	for _, userID := range mentions.UserIDs {
		networkID, ok := nc.bridge.Matrix.ParseGhostMXID(userID)
		if !ok {
			continue
		}
		ref, ok := xmppReferenceForJID(UnescapeJID(string(networkID)))
		if !ok {
			continue
		}
		if _, ok := seen[ref.URI]; ok {
			continue
		}
		seen[ref.URI] = struct{}{}
		refs = append(refs, ref)
	}
	return refs
}
