// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"encoding/base64"
	"encoding/xml"
	"strings"
	"unicode"

	"gosrc.io/xmpp/stanza"
)

// NSBob is XEP-0231 Bits of Binary.
const NSBob = "urn:xmpp:bob"

// maxXMPPBobSize bounds inline media accepted from the remote side.
const maxXMPPBobSize = 5 * 1024 * 1024

// XMPPBobData carries inline binary data referenced by a cid: URI.
type XMPPBobData struct {
	stanza.MsgExtension
	XMLName xml.Name `xml:"urn:xmpp:bob data"`
	CID     string   `xml:"cid,attr,omitempty"`
	Type    string   `xml:"type,attr,omitempty"`
	Data    string   `xml:",chardata"`
}

func init() {
	stanza.TypeRegistry.MapExtension(stanza.PKTMessage, xml.Name{Space: NSBob, Local: "data"}, XMPPBobData{})
}

func stripASCIIWhitespace(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}

// extractXMPPBob returns decoded inline binary data from the first usable
// XEP-0231 data element in the message.
func extractXMPPBob(msg stanza.Message) (data []byte, mimeType, cid string, ok bool) {
	for _, ext := range msg.Extensions {
		var bob *XMPPBobData
		switch typed := ext.(type) {
		case *XMPPBobData:
			bob = typed
		case XMPPBobData:
			value := typed
			bob = &value
		}
		if bob == nil {
			continue
		}
		cid = strings.TrimSpace(bob.CID)
		mimeType = strings.TrimSpace(bob.Type)
		if cid == "" {
			continue
		}
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		encoded := stripASCIIWhitespace(bob.Data)
		if encoded == "" {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(decoded) == 0 || len(decoded) > maxXMPPBobSize {
			continue
		}
		return decoded, mimeType, cid, true
	}
	return nil, "", "", false
}

// bobInlineFileName picks a stable file name for inline media based on its
// mime type.
// cidFallbackBody replaces a bare cid: reference (an inline image the sender
// did not include as XEP-0231 BOB data) with a stable fallback text so raw
// "cid:..." tokens are not bridged as message bodies.
func cidFallbackBody(body string) (string, bool) {
	trimmed := strings.TrimSpace(body)
	if len(trimmed) < 4 || !strings.EqualFold(trimmed[:4], "cid:") {
		return body, false
	}
	ref := strings.TrimSpace(trimmed[4:])
	if ref == "" {
		return "📎 Inline image is not available", true
	}
	return "📎 Inline image is not available (" + ref + ")", true
}

func bobInlineFileName(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/png":
		return "inline.png"
	case "image/jpeg", "image/jpg":
		return "inline.jpg"
	case "image/gif":
		return "inline.gif"
	case "image/webp":
		return "inline.webp"
	default:
		return "inline.bin"
	}
}
