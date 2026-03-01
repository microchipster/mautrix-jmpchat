package connector

import (
	"strings"

	"maunium.net/go/mautrix/bridgev2/networkid"
)

// EscapeJID converts an XMPP JID into a format that is safe for Matrix User IDs,
// since mautrix-go's EncodeUserLocalpart currently leaves '+' unescaped, which Synapse rejects.
// EncodeUserLocalpart will encode '=' into '=3d', so we cannot use '=' to escape.
func EscapeJID(jid string) string {
	return strings.ReplaceAll(jid, "+", "._plus_.")
}

// UnescapeJID converts an escaped JID back to its original form.
func UnescapeJID(escaped string) string {
	return strings.ReplaceAll(escaped, "._plus_.", "+")
}

func MakeUserID(jid string) networkid.UserID {
	return networkid.UserID(EscapeJID(jid))
}

func MakePortalID(jid string) networkid.PortalID {
	return networkid.PortalID(EscapeJID(jid))
}
