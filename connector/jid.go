// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"sort"
	"strings"

	"maunium.net/go/mautrix/bridgev2/networkid"
)

// CanonicalBareJID cleans and canonicalizes an XMPP JID.
// It strips resource parts, trims spaces, normalizes phone numbers to +digits@cheogram.com,
// lowercases domain names, and sorts comma-separated group contact lists for cheogram.com.
func CanonicalBareJID(jid string) string {
	jid = strings.TrimSpace(jid)
	if jid == "" {
		return ""
	}
	// Strip resource
	jid = strings.SplitN(jid, "/", 2)[0]
	jid = strings.TrimSpace(jid)
	if jid == "" {
		return ""
	}
	// Check if phone-only format without @
	if !strings.Contains(jid, "@") {
		var digits strings.Builder
		for _, r := range jid {
			if r >= '0' && r <= '9' {
				digits.WriteRune(r)
			}
		}
		if digits.Len() > 0 {
			return "+" + digits.String() + "@cheogram.com"
		}
		return jid
	}

	parts := strings.SplitN(jid, "@", 2)
	localPart := strings.TrimSpace(parts[0])
	domainPart := strings.ToLower(strings.TrimSpace(parts[1]))

	// For cheogram.com, ensure phone numbers have + and sort group contacts
	if domainPart == "cheogram.com" {
		if strings.Contains(localPart, ",") {
			contactList := strings.Split(localPart, ",")
			cleaned := make([]string, 0, len(contactList))
			for _, c := range contactList {
				c = strings.TrimSpace(c)
				if c == "" {
					continue
				}
				if !strings.HasPrefix(c, "+") {
					c = "+" + c
				}
				cleaned = append(cleaned, c)
			}
			sort.Strings(cleaned)
			return strings.Join(cleaned, ",") + "@cheogram.com"
		}
		if !strings.HasPrefix(localPart, "+") {
			allDigits := true
			for _, r := range localPart {
				if r < '0' || r > '9' {
					allDigits = false
					break
				}
			}
			if allDigits && len(localPart) > 0 {
				localPart = "+" + localPart
			}
		}
	}

	return localPart + "@" + domainPart
}

// EscapeJID converts an XMPP JID into a format that is safe for Matrix User IDs,
// since mautrix-go's EncodeUserLocalpart currently leaves '+' unescaped, which Synapse rejects.
// EncodeUserLocalpart encodes '_' into '__', so we avoid using underscores in our escape sequence.
func EscapeJID(jid string) string {
	j := strings.ReplaceAll(jid, "+", "=2b")
	return strings.ReplaceAll(j, ",", "=2c")
}

// UnescapeJID converts an escaped JID back to its original form.
func UnescapeJID(escaped string) string {
	val := strings.ReplaceAll(escaped, "=2b", "+")
	return strings.ReplaceAll(val, "=2c", ",")
}

func MakeUserID(jid string) networkid.UserID {
	return networkid.UserID(EscapeJID(CanonicalBareJID(jid)))
}

func MakePortalID(jid string) networkid.PortalID {
	return networkid.PortalID(EscapeJID(CanonicalBareJID(jid)))
}

