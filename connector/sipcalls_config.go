//go:build sipcalls

// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"github.com/williamkray/mrtc-sip-bridge/pkg/hostkit"
)

// toHostkit converts the bridge's `network.calls` config into the library's hostkit.Config.
// The two structs mirror each other field by field (same YAML key names); the Dialpad widget
// block is intentionally not exposed yet, so widget remains disabled.
func (c CallsConfig) toHostkit() hostkit.Config {
	return hostkit.Config{
		SIPBindIP:              c.SIPBindIP,
		SIPPortMin:             c.SIPPortMin,
		SIPPortMax:             c.SIPPortMax,
		ExternalIP:             c.ExternalIP,
		STUNServer:             c.STUNServer,
		RTPPortMin:             c.RTPPortMin,
		RTPPortMax:             c.RTPPortMax,
		RegisterExpirySeconds:  c.RegisterExpirySeconds,
		RegisterRefreshSeconds: c.RegisterRefreshSeconds,
		RingTimeoutSeconds:     c.RingTimeoutSeconds,
		RTPTimeoutSeconds:      c.RTPTimeoutSeconds,
		LiveKitServiceURL:      c.LiveKitServiceURL,
		DefaultCountryCode:     c.DefaultCountryCode,
		OutboundTrigger:        c.OutboundTrigger,
		DialTimeoutSeconds:     c.DialTimeoutSeconds,
		DialAllowedPrefixes:    c.DialAllowedPrefixes,
		DialNumberFormat:       c.DialNumberFormat,
		AllowSelfDial:          c.AllowSelfDial,
		DTMFRedactCommand:      c.DTMFRedactCommand,
	}
}
