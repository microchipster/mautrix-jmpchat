// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

// CallsConfig mirrors the `network.calls` section of the embedded calls core
// (mrtc-sip-bridge hostkit configuration). It lives in the default build so the YAML
// config parses everywhere; the `sipcalls` build converts it to hostkit.Config
// (connector/sipcalls_config.go). Field names and defaults follow the library.
import (
	"strings"
)

type CallsConfig struct {
	// Enabled turns the SIP calling support on. The image always ships the
	// feature and it defaults to on; unset means "available" (accounts still
	// opt in individually with `sip-login`). Set `false` to disable the SIP
	// runtime entirely.
	Enabled *bool `yaml:"enabled,omitempty"`

	SIPBindIP              string   `yaml:"sip_bind_ip,omitempty"`
	SIPPortMin             int      `yaml:"sip_port_min,omitempty"`
	SIPPortMax             int      `yaml:"sip_port_max,omitempty"`
	ExternalIP             string   `yaml:"external_ip,omitempty"`
	STUNServer             string   `yaml:"stun_server,omitempty"`
	RTPPortMin             int      `yaml:"rtp_port_min,omitempty"`
	RTPPortMax             int      `yaml:"rtp_port_max,omitempty"`
	RegisterExpirySeconds  int      `yaml:"register_expiry_seconds,omitempty"`
	RegisterRefreshSeconds int      `yaml:"register_refresh_seconds,omitempty"`
	RingTimeoutSeconds     int      `yaml:"ring_timeout_seconds,omitempty"`
	RTPTimeoutSeconds      int      `yaml:"rtp_timeout_seconds,omitempty"`
	LiveKitServiceURL      string   `yaml:"livekit_service_url,omitempty"`
	DefaultCountryCode     string   `yaml:"default_country_code,omitempty"`
	OutboundTrigger        string   `yaml:"outbound_trigger,omitempty"`
	DialTimeoutSeconds     int      `yaml:"dial_timeout_seconds,omitempty"`
	DialAllowedPrefixes    []string `yaml:"dial_allowed_prefixes,omitempty"`
	DialNumberFormat       string   `yaml:"dial_number_format,omitempty"`
	AllowSelfDial          bool     `yaml:"allow_self_dial,omitempty"`
	DTMFRedactCommand      *bool    `yaml:"dtmf_redact_command,omitempty"`
	// AutoCreateCallRoom creates a dedicated unencrypted call room on demand when a call cannot
	// ring in the portal room because it is encrypted. The zero value (unset) means enabled:
	// the bridge creates the call room and points you at it. Set `false` to refuse to ring and
	// point to the `call-room` command instead.
	AutoCreateCallRoom *bool `yaml:"auto_create_call_room,omitempty"`

	// RoomMode selects the room calls ring in. The default ("portal") rings in the
	// phone number's own chat room, including encrypted ones: the bridge distributes
	// SFrame call keys to the room members. Set "call-room" to keep the legacy
	// separate unencrypted call room.
	RoomMode string `yaml:"room_mode,omitempty"`
	// E2EE controls SFrame call encryption in encrypted rooms. "auto" (the default)
	// encrypts; "off" disables it, and encrypted portals then fall back to a call room.
	E2EE string `yaml:"e2ee,omitempty"`
	// PickleKey protects the call-device Olm accounts in the database. Leave empty to
	// derive a key from the appservice token.
	PickleKey string `yaml:"pickle_key,omitempty"`

	// Widget configures the optional Dialpad widget/PWA served by the bridge.
	Widget CallsWidgetConfig `yaml:"widget,omitempty"`
}

// CallRoomMode is the room calls ring in.
type CallRoomMode string

const (
	// CallRoomModePortal rings in the phone number's chat room (default).
	CallRoomModePortal CallRoomMode = "portal"
	// CallRoomModeCallRoom keeps the legacy separate unencrypted call room.
	CallRoomModeCallRoom CallRoomMode = "call-room"
)

// CallRoomMode returns the configured room mode (portal unless call-room was asked for).
func (c CallsConfig) CallRoomMode() CallRoomMode {
	if strings.EqualFold(strings.TrimSpace(c.RoomMode), string(CallRoomModeCallRoom)) {
		return CallRoomModeCallRoom
	}
	return CallRoomModePortal
}

// E2EEEnabled reports whether call media should be SFrame-encrypted in encrypted rooms.
func (c CallsConfig) E2EEEnabled() bool {
	return !strings.EqualFold(strings.TrimSpace(c.E2EE), "off")
}

// IsEnabled reports whether the SIP calling runtime should start. The zero
// value (unset) means enabled; only an explicit `false` disables it.
func (c CallsConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

// IsAutoCreateCallRoom reports whether a call room should be created lazily
// when a call arrives for an encrypted portal. The zero value (unset) means
// enabled; only an explicit `false` restores the refuse-and-point behavior.
func (c CallsConfig) IsAutoCreateCallRoom() bool {
	return c.AutoCreateCallRoom == nil || *c.AutoCreateCallRoom
}

// CallsWidgetConfig mirrors hostkit.WidgetConfig for the `network.calls.widget` section.
type CallsWidgetConfig struct {
	// Enabled turns the Dialpad widget and its client-API surface on.
	Enabled bool `yaml:"enabled"`
	// PublicURL is the externally reachable base URL of the widget. Required when enabled.
	PublicURL string `yaml:"public_url,omitempty"`
	// Secret signs widget device tokens. The value "generate" makes the bridge create one on first start.
	Secret string `yaml:"secret,omitempty"`
	// FrameAncestors lists extra hosts allowed to embed the widget in an iframe.
	FrameAncestors []string `yaml:"frame_ancestors,omitempty"`
	// AllowRemoteUsers also lets users from other homeservers in the appservice namespace use the widget.
	AllowRemoteUsers bool `yaml:"allow_remote_users,omitempty"`
	// RemoveWhenDisabled asks the bridge to actively remove widget state when the widget is turned off.
	RemoveWhenDisabled bool `yaml:"remove_when_disabled,omitempty"`
	// TrustedProxies lists proxy addresses whose X-Forwarded-* headers are honored.
	TrustedProxies []string `yaml:"trusted_proxies,omitempty"`
	// PWA configures the progressive-web-app mode (dialpad links that enroll devices).
	PWA CallsWidgetPWAConfig `yaml:"pwa,omitempty"`
}

// CallsWidgetPWAConfig mirrors hostkit.PWAConfig.
type CallsWidgetPWAConfig struct {
	Enabled              bool `yaml:"enabled"`
	InactivityDays       int  `yaml:"inactivity_days,omitempty"`
	MaxLifetimeDays      int  `yaml:"max_lifetime_days,omitempty"`
	RotationGraceSeconds int  `yaml:"rotation_grace_seconds,omitempty"`
	EnrollMinutes        int  `yaml:"enroll_minutes,omitempty"`
	MaxLinksPerUser      int  `yaml:"max_links_per_user,omitempty"`
	// NotifyOnEnroll sends a management-room notice when a new device enrolls.
	NotifyOnEnroll *bool `yaml:"notify_on_enroll,omitempty"`
}
