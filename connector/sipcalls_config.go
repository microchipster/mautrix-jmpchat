//go:build sipcalls

// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"github.com/williamkray/mrtc-sip-bridge/pkg/hostkit"
)

// toHostkit converts the bridge's `network.calls` config into the library's hostkit.Config.
// The two structs mirror each other field by field (same YAML key names).
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
		Widget: hostkit.WidgetConfig{
			Enabled:            c.Widget.Enabled,
			PublicURL:          c.Widget.PublicURL,
			Secret:             c.Widget.Secret,
			FrameAncestors:     c.Widget.FrameAncestors,
			AllowRemoteUsers:   c.Widget.AllowRemoteUsers,
			RemoveWhenDisabled: c.Widget.RemoveWhenDisabled,
			TrustedProxies:     c.Widget.TrustedProxies,
			PWA: hostkit.PWAConfig{
				Enabled:              c.Widget.PWA.Enabled,
				InactivityDays:       c.Widget.PWA.InactivityDays,
				MaxLifetimeDays:      c.Widget.PWA.MaxLifetimeDays,
				RotationGraceSeconds: c.Widget.PWA.RotationGraceSeconds,
				EnrollMinutes:        c.Widget.PWA.EnrollMinutes,
				MaxLinksPerUser:      c.Widget.PWA.MaxLinksPerUser,
				NotifyOnEnroll:       c.Widget.PWA.NotifyOnEnroll,
			},
		},
	}
}
