package connector

import (
	"strings"

	"maunium.net/go/mautrix/bridgev2/networkid"
)

// EscapeJID converts an XMPP JID into a format that is safe for Matrix User IDs,
// since mautrix-go's EncodeUserLocalpart currently leaves '+' unescaped, which Synapse rejects.
func EscapeJID(jid string) string {
	jid = strings.ReplaceAll(jid, "=", "=3d")
	jid = strings.ReplaceAll(jid, "+", "=2b")
	return jid
}

// UnescapeJID converts an escaped JID back to its original form.
func UnescapeJID(escaped string) string {
	escaped = strings.ReplaceAll(escaped, "=2b", "+")
	escaped = strings.ReplaceAll(escaped, "=3d", "=")
	return escaped
}

func MakeUserID(jid string) networkid.UserID {
	return networkid.UserID(EscapeJID(jid))
}

func MakePortalID(jid string) networkid.PortalID {
	return networkid.PortalID(EscapeJID(jid))
}
