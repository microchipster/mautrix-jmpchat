package connector

import (
	"strings"

	"maunium.net/go/mautrix/bridgev2/networkid"
)

// EscapeJID converts an XMPP JID into a format that is safe for Matrix User IDs,
// since mautrix-go's EncodeUserLocalpart currently leaves '+' unescaped, which Synapse rejects.
// EncodeUserLocalpart encodes '_' into '__', so we avoid using underscores in our escape sequence.
func EscapeJID(jid string) string {
	return strings.ReplaceAll(jid, "+", "=2b")
}

// UnescapeJID converts an escaped JID back to its original form.
func UnescapeJID(escaped string) string {
	return strings.ReplaceAll(escaped, "=2b", "+")
}

func MakeUserID(jid string) networkid.UserID {
	return networkid.UserID(EscapeJID(jid))
}

func MakePortalID(jid string) networkid.PortalID {
	return networkid.PortalID(EscapeJID(jid))
}
