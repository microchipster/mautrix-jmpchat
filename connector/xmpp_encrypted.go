// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"encoding/xml"
	"strings"

	"gosrc.io/xmpp/stanza"
)

const (
	NSOMEMO            = "eu.siacs.conversations.axolotl"
	NSOpenPGPEncrypted = "jabber:x:encrypted"
)

// XMPPOMEMOEncrypted is the OMEMO (XEP-0384) encrypted payload element.
type XMPPOMEMOEncrypted struct {
	stanza.MsgExtension
	XMLName xml.Name `xml:"eu.siacs.conversations.axolotl encrypted"`
}

// XMPPOpenPGPEncrypted is the legacy OpenPGP encryption element.
type XMPPOpenPGPEncrypted struct {
	stanza.MsgExtension
	XMLName xml.Name `xml:"jabber:x:encrypted x"`
}

func init() {
	stanza.TypeRegistry.MapExtension(stanza.PKTMessage, xml.Name{Space: NSOMEMO, Local: "encrypted"}, XMPPOMEMOEncrypted{})
	stanza.TypeRegistry.MapExtension(stanza.PKTMessage, xml.Name{Space: NSOpenPGPEncrypted, Local: "x"}, XMPPOpenPGPEncrypted{})
}

// encryptedXMPPKind reports whether a message carries end-to-end encrypted
// content that this bridge cannot decrypt, and names the scheme when known.
func encryptedXMPPKind(msg stanza.Message) (string, bool) {
	var omemo XMPPOMEMOEncrypted
	if msg.Get(&omemo) {
		return "OMEMO", true
	}
	var openPGP XMPPOpenPGPEncrypted
	if msg.Get(&openPGP) {
		return "OpenPGP", true
	}
	var eme XMPPEME
	if msg.Get(&eme) {
		return emeDisplayName(eme.Namespace), true
	}
	body := strings.ToLower(strings.TrimSpace(stanza.StringValue(msg.Body)))
	if strings.HasPrefix(body, "aesgcm://") {
		return "AES-GCM media", true
	}
	return "", false
}

// emeDisplayName maps XEP-0380 encryption namespaces to friendly names.
func emeDisplayName(namespace string) string {
	switch strings.TrimSpace(namespace) {
	case "eu.siacs.conversations.axolotl", "urn:xmpp:omemo:0", "urn:xmpp:omemo:1", "urn:xmpp:omemo:2":
		return "OMEMO"
	case "jabber:x:encrypted", "urn:xmpp:openpgp:0":
		return "OpenPGP"
	case "urn:xmpp:otr:0":
		return "OTR"
	case "":
		return "end-to-end encryption"
	default:
		return "end-to-end encryption"
	}
}

// encryptedXMPPNotice is the fixed placeholder bridged for encrypted messages.
func encryptedXMPPNotice(kind string) string {
	if kind == "" {
		kind = "end-to-end encryption"
	}
	return "🔒 Encrypted XMPP message (" + kind + ") — this bridge cannot decrypt it."
}
