// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

// CallsConfig mirrors the `network.calls` section of the embedded calls core
// (mrtc-sip-bridge hostkit configuration). It lives in the default build so the YAML
// config parses everywhere; the `sipcalls` build converts it to hostkit.Config
// (connector/sipcalls_config.go). Field names and defaults follow the library.
type CallsConfig struct {
	// Enabled turns the SIP calling support on. Off by default; the extension stays inert.
	Enabled bool `yaml:"enabled"`

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
	// ring in the portal room because it is encrypted. Off by default: with it off the bridge
	// refuses to ring and points to the `call-room` command instead.
	AutoCreateCallRoom bool `yaml:"auto_create_call_room,omitempty"`
}
