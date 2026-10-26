// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"strings"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"
)

// mentionPill is one user pill (mention) found in a Matrix formatted_body.
type mentionPill struct {
	displayname string
	mxid        id.UserID
}

// newMentionParser returns an HTML parser with the settings of format.HTMLToText whose PillConverter
// calls onPill for every user pill and renders the pill with the text it returns.
func newMentionParser(onPill func(displayname string, mxid id.UserID) string) *format.HTMLParser {
	return &format.HTMLParser{
		TabsToSpaces:   4,
		Newline:        "\n",
		HorizontalLine: "\n---\n",
		PillConverter: func(displayname, mxid, eventID string, ctx format.Context) string {
			if len(mxid) > 0 && mxid[0] == '@' {
				if s := onPill(displayname, id.UserID(mxid)); s != "" || displayname == "" {
					return s
				}
				return displayname
			}
			return format.DefaultPillConverter(displayname, mxid, eventID, ctx)
		},
	}
}

// rewriteMentions makes the XMPP body of a Matrix message use the XMPP-side names of mentioned bridged
// users instead of their Matrix display names (which carry the appservice suffix). Without HTML
// formatting the body is returned unchanged. Otherwise each user pill that resolve knows replaces one
// occurrence of its display name or raw user ID in body (in pill order); an empty body (HTML-only event)
// is derived from the formatted body with the resolved names.
func rewriteMentions(body, formattedBody string, isHTML bool, resolve func(id.UserID) (string, bool)) string {
	if formattedBody == "" || !isHTML {
		return body
	}
	ctx := format.NewContext(context.Background())
	if body == "" {
		parser := newMentionParser(func(displayname string, mxid id.UserID) string {
			if name, ok := resolve(mxid); ok && name != "" {
				return name
			}
			return displayname
		})
		return strings.TrimSpace(parser.Parse(formattedBody, ctx))
	}
	var pills []mentionPill
	parser := newMentionParser(func(displayname string, mxid id.UserID) string {
		pills = append(pills, mentionPill{displayname, mxid})
		return displayname
	})
	parser.Parse(formattedBody, ctx)
	// Pills are replaced in document order, each at its next occurrence (display name or raw user ID)
	// after the previous replacement, so the same text elsewhere in the body (not a pill, e.g. an edited
	// pill text that is a common word) stays unchanged.
	var sb strings.Builder
	pos := 0
	for _, p := range pills {
		name, ok := resolve(p.mxid)
		if !ok || name == "" {
			continue
		}
		at, n := -1, 0
		if p.displayname != "" {
			if i := strings.Index(body[pos:], p.displayname); i >= 0 {
				at, n = i, len(p.displayname)
			}
		}
		if i := strings.Index(body[pos:], string(p.mxid)); i >= 0 && (at < 0 || i < at) {
			at, n = i, len(p.mxid)
		}
		if at < 0 {
			continue
		}
		sb.WriteString(body[pos : pos+at])
		sb.WriteString(name)
		pos += at + n
	}
	sb.WriteString(body[pos:])
	return sb.String()
}

// mentionResolver returns the resolver used by rewriteMentions for a portal: a bridged ghost user ID
// maps to the XMPP-side name (MUC occupant nick, own login localpart, roster/contact name, Cheogram
// number, else the JID localpart); other user IDs are not resolved.
func (nc *MyNetworkClient) mentionResolver(ctx context.Context, portal *bridgev2.Portal) func(id.UserID) (string, bool) {
	if nc == nil || nc.bridge == nil || nc.bridge.Matrix == nil {
		return func(id.UserID) (string, bool) { return "", false }
	}
	return func(mxid id.UserID) (string, bool) {
		ghostID, ok := nc.bridge.Matrix.ParseGhostMXID(mxid)
		if !ok || ghostID == "" {
			return "", false
		}
		name := nc.xmppNameForGhost(ctx, ghostID)
		if name == "" {
			return "", false
		}
		return name, true
	}
}

// xmppNameForGhost is the name an XMPP client would show for the ghost's JID.
func (nc *MyNetworkClient) xmppNameForGhost(ctx context.Context, ghostID networkid.UserID) string {
	j := UnescapeJID(string(ghostID))
	if i := strings.IndexByte(j, '/'); i >= 0 {
		if nick := strings.TrimSpace(j[i+1:]); nick != "" {
			return nick
		}
		j = j[:i]
	}
	local := j
	if i := strings.IndexByte(j, '@'); i >= 0 {
		local = j[:i]
	}
	if nc != nil && nc.login != nil && strings.EqualFold(CanonicalBareJID(j), strings.ToLower(string(nc.login.ID))) {
		return local
	}
	if name := contactDisplayName(ctx, nc.bridgeOrNil(), j); name != "" {
		return name
	}
	if num, ok := e164FromCheogramJID(j); ok {
		return num
	}
	if local == "" {
		return j
	}
	return local
}

// bridgeOrNil returns the bridge reference for helpers that accept a nil bridge.
func (nc *MyNetworkClient) bridgeOrNil() *bridgev2.Bridge {
	if nc == nil {
		return nil
	}
	return nc.bridge
}
