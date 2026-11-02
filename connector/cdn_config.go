// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import "strings"

// CdnConfig configures the optional bridge file service used for outbound
// attachments (network.cdn). When both the base URL and the upload token are
// set, outgoing media is encrypted locally, uploaded as ciphertext and sent
// as a zero-knowledge link instead of the remote side's own file service.
type CdnConfig struct {
	// BaseURL is the public base of the file service, e.g.
	// https://cdn.example.com. The bridge appends /f.
	BaseURL string `yaml:"base_url,omitempty"`
	// UploadToken is the bearer token accepted by the file service
	// (cdn.upload_token on the bridge that hosts it).
	UploadToken string `yaml:"upload_token,omitempty"`
	// SendView is the view policy for the links this bridge sends: "public"
	// (default; anyone with the link can decrypt it) or "first" (only the
	// first browser that opens it). Recipients are usually plain browsers or
	// XMPP clients, which cannot produce a viewer fingerprint, so the
	// default stays public.
	SendView string `yaml:"send_view,omitempty"`
}

// Enabled reports whether outbound CDN sending is configured.
func (c CdnConfig) Enabled() bool {
	return strings.TrimSpace(c.BaseURL) != "" && strings.TrimSpace(c.UploadToken) != ""
}

// View returns the view policy to request for sent links.
func (c CdnConfig) View() string {
	if strings.EqualFold(strings.TrimSpace(c.SendView), "first") {
		return "first"
	}
	return "public"
}
