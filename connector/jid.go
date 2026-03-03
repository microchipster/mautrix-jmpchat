package connector

import (
	"strings"

	"maunium.net/go/mautrix/bridgev2/networkid"
)

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
	return networkid.UserID(EscapeJID(jid))
}

func MakePortalID(jid string) networkid.PortalID {
	return networkid.PortalID(EscapeJID(jid))
}
