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
	jid = strings.TrimSpace(UnescapeJID(jid))
	if jid == "" {
		return ""
	}
	// Strip resource
	jid = strings.SplitN(jid, "/", 2)[0]
	jid = strings.TrimSpace(jid)
	if jid == "" {
		return ""
	}
	// Check phone-only input without @. Cheogram uses E.164-like JIDs; local
	// US numbers without a country code are unambiguous enough to assume +1.
	if !strings.Contains(jid, "@") {
		if isPhoneLike(jid) {
			local := canonicalCheogramLocalPart(jid)
			return local + "@cheogram.com"
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
			seen := make(map[string]struct{}, len(contactList))
			cleaned := make([]string, 0, len(contactList))
			for _, c := range contactList {
				c = strings.TrimSpace(c)
				if c == "" {
					continue
				}
				c = canonicalCheogramLocalPart(c)
				if _, ok := seen[c]; ok {
					continue
				}
				seen[c] = struct{}{}
				cleaned = append(cleaned, c)
			}
			sort.Strings(cleaned)
			return strings.Join(cleaned, ",") + "@cheogram.com"
		}
		localPart = canonicalCheogramLocalPart(localPart)
	}

	return localPart + "@" + domainPart
}

// stripPhoneURIParameters strips RFC 3966 / URI parameters (such as ;phone-context=...)
// from a phone number or localpart.
func stripPhoneURIParameters(value string) string {
	if idx := strings.Index(value, ";"); idx != -1 {
		return strings.TrimSpace(value[:idx])
	}
	return strings.TrimSpace(value)
}

func isPhoneLike(value string) bool {
	value = stripPhoneURIParameters(value)
	if value == "" {
		return false
	}
	hasDigit := false
	for i, r := range value {
		switch {
		case r >= '0' && r <= '9':
			hasDigit = true
		case r == '+' && i == 0:
		case r == '(' || r == ')' || r == '-' || r == '.' || r == ' ' || r == '\t':
		default:
			return false
		}
	}
	return hasDigit
}

func canonicalCheogramLocalPart(localPart string) string {
	localPart = stripPhoneURIParameters(localPart)
	if !isPhoneLike(localPart) {
		return strings.ToLower(localPart)
	}
	hasCountryPrefix := strings.HasPrefix(localPart, "+")
	var digits strings.Builder
	for _, r := range localPart {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		}
	}
	value := digits.String()
	if value == "" {
		return ""
	}
	// Short codes (3-6 digits) without explicit '+' should stay raw short codes
	if !hasCountryPrefix && len(value) >= 3 && len(value) <= 6 {
		return value
	}
	if !hasCountryPrefix && (len(value) == 7 || len(value) == 10) {
		value = "1" + value
	}
	return "+" + value
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

// isBareServerAddress reports whether a JID/portal ID is a bare server address
// (a domain without a localpart), such as the source of Snikket spam-report
// server notices. These addresses are services, not contacts.
func isBareServerAddress(jid string) bool {
	return jid != "" && !strings.Contains(jid, "@") && strings.Contains(jid, ".")
}

// isCheogramGatewayJID reports whether a JID belongs to the Cheogram/JMP gateway
// rather than to a real XMPP MUC service. Cheogram addresses always use the
// cheogram.com domain, either for a single contact (+15551234567@cheogram.com),
// for a group text (+15551234567,+15557654321@cheogram.com) or as the bare
// gateway JID (cheogram.com). None of them are MUC rooms: the gateway expects
// type=chat and must not receive MUC join presence, configuration or bookmarks.
func isCheogramGatewayJID(jid string) bool {
	canonical := strings.ToLower(CanonicalBareJID(UnescapeJID(strings.TrimSpace(jid))))
	if canonical == "" {
		return false
	}
	return canonical == "cheogram.com" || strings.HasSuffix(canonical, "@cheogram.com")
}

// isSelfBareJID reports whether a JID is the given account's own bare JID.
// A login's own JID is never a MUC room: a stray bookmark or an autojoin entry
// for it would make the join watchdog send MUC join presences to the account
// itself, which the server silently drops and the watchdog retries forever.
func isSelfBareJID(ownBareJID, jid string) bool {
	own := CanonicalBareJID(UnescapeJID(strings.TrimSpace(ownBareJID)))
	if own == "" {
		return false
	}
	candidate := CanonicalBareJID(UnescapeJID(strings.TrimSpace(jid)))
	return candidate != "" && strings.EqualFold(candidate, own)
}

// e164FromCheogramJID extracts an E.164 phone number from a Cheogram phone JID
// ("+15551234567@cheogram.com", possibly carrying phone-context parameters). Short codes and
// comma-separated group JIDs are rejected; the boolean reports whether the input was usable.
func e164FromCheogramJID(jid string) (string, bool) {
	canonical := CanonicalBareJID(UnescapeJID(strings.TrimSpace(jid)))
	if canonical == "" || !strings.HasSuffix(canonical, "@cheogram.com") {
		return "", false
	}
	local := strings.TrimSuffix(canonical, "@cheogram.com")
	local = stripPhoneURIParameters(local)
	if !strings.HasPrefix(local, "+") || len(local) < 2 {
		return "", false
	}
	for _, r := range local[1:] {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	return local, true
}
