//go:build sipcalls

// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"reflect"
	"strings"
	"testing"

	"github.com/williamkray/mrtc-sip-bridge/pkg/sipaccount"
	"go.mau.fi/util/ptr"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/id"
)

func TestCallsConfigToHostkit(t *testing.T) {
	t.Parallel()
	redact := true
	cfg := CallsConfig{
		Enabled:                ptr.Ptr(true),
		SIPBindIP:              "192.0.2.10",
		SIPPortMin:             5080,
		SIPPortMax:             5089,
		ExternalIP:             "198.51.100.7",
		STUNServer:             "stun.example.com:3478",
		RTPPortMin:             42000,
		RTPPortMax:             42099,
		RegisterExpirySeconds:  120,
		RegisterRefreshSeconds: 30,
		RingTimeoutSeconds:     45,
		RTPTimeoutSeconds:      90,
		LiveKitServiceURL:      "https://rtc.example.com",
		DefaultCountryCode:     "44",
		OutboundTrigger:        "ring",
		DialTimeoutSeconds:     75,
		DialAllowedPrefixes:    []string{"+1", "+44"},
		DialNumberFormat:       "digits",
		AllowSelfDial:          true,
		DTMFRedactCommand:      &redact,
	}
	hk := cfg.toHostkit()
	if hk.SIPBindIP != cfg.SIPBindIP || hk.SIPPortMin != cfg.SIPPortMin || hk.SIPPortMax != cfg.SIPPortMax {
		t.Fatalf("SIP network fields not mapped: %#v", hk)
	}
	if hk.ExternalIP != cfg.ExternalIP || hk.STUNServer != cfg.STUNServer {
		t.Fatalf("SIP NAT fields not mapped: %#v", hk)
	}
	if hk.RTPPortMin != cfg.RTPPortMin || hk.RTPPortMax != cfg.RTPPortMax {
		t.Fatalf("RTP port fields not mapped: %#v", hk)
	}
	if hk.RegisterExpirySeconds != cfg.RegisterExpirySeconds || hk.RegisterRefreshSeconds != cfg.RegisterRefreshSeconds {
		t.Fatalf("registration timing fields not mapped: %#v", hk)
	}
	if hk.RingTimeoutSeconds != cfg.RingTimeoutSeconds || hk.RTPTimeoutSeconds != cfg.RTPTimeoutSeconds {
		t.Fatalf("timeout fields not mapped: %#v", hk)
	}
	if hk.LiveKitServiceURL != cfg.LiveKitServiceURL || hk.DefaultCountryCode != cfg.DefaultCountryCode {
		t.Fatalf("service URL / country code not mapped: %#v", hk)
	}
	if hk.OutboundTrigger != cfg.OutboundTrigger || hk.DialTimeoutSeconds != cfg.DialTimeoutSeconds {
		t.Fatalf("dial fields not mapped: %#v", hk)
	}
	if !reflect.DeepEqual(hk.DialAllowedPrefixes, cfg.DialAllowedPrefixes) {
		t.Fatalf("prefix allowlist not mapped: %#v", hk.DialAllowedPrefixes)
	}
	if hk.DialNumberFormat != cfg.DialNumberFormat || !hk.AllowSelfDial {
		t.Fatalf("dial number format / self-dial not mapped: %#v", hk)
	}
	if hk.DTMFRedactCommand == nil || !*hk.DTMFRedactCommand {
		t.Fatalf("DTMF redact flag not mapped: %#v", hk.DTMFRedactCommand)
	}
}

func TestCallsConfigEnabledDefault(t *testing.T) {
	t.Parallel()
	if (CallsConfig{}).IsEnabled() != true {
		t.Fatal("calling should be enabled by default (unset enabled)")
	}
	if (CallsConfig{Enabled: ptr.Ptr(false)}).IsEnabled() {
		t.Fatal("explicit enabled: false should disable calling")
	}
	if !(CallsConfig{Enabled: ptr.Ptr(true)}).IsEnabled() {
		t.Fatal("explicit enabled: true should enable calling")
	}
}

func TestCallsConfigDefaultsValidate(t *testing.T) {
	t.Parallel()
	hk := CallsConfig{Enabled: ptr.Ptr(true)}.toHostkit()
	hk.ApplyDefaults()
	if hk.SIPPortMin == 0 || hk.RTPPortMin == 0 || hk.RingTimeoutSeconds == 0 || hk.DefaultCountryCode == "" {
		t.Fatalf("expected the library defaults to be applied: %#v", hk)
	}
	if err := hk.Validate("network.calls."); err != nil {
		t.Fatalf("default calls config must validate: %v", err)
	}
}

func TestSIPCredsRoundTrip(t *testing.T) {
	t.Parallel()
	if _, ok := sipCreds(nil); ok {
		t.Fatal("nil metadata must not produce credentials")
	}
	meta := &LoginMetadata{}
	if _, ok := sipCreds(meta); ok {
		t.Fatal("empty metadata must not produce credentials")
	}
	creds := sipaccount.Credentials{
		Username:      "alice",
		Password:      "secret",
		Registrar:     "sip.example.com:5060",
		Transport:     "udp",
		DisplayNumber: "+15551234567",
	}
	setSIPCreds(meta, creds)
	got, ok := sipCreds(meta)
	if !ok {
		t.Fatal("expected stored credentials to be complete")
	}
	if got != creds {
		t.Fatalf("credentials round trip mismatch:\n got %#v\nwant %#v", got, creds)
	}
	meta.SIPCalls.Password = ""
	if _, ok := sipCreds(meta); ok {
		t.Fatal("incomplete credentials must be rejected")
	}
	setSIPCreds(nil, creds)
	clearSIPCreds(meta)
	if meta.SIPCalls != nil {
		t.Fatal("clearSIPCreds must remove the stored entry")
	}
	clearSIPCreds(nil)
}

func TestIsCallPortal(t *testing.T) {
	t.Parallel()
	x := &callsExt{}
	login := networkid.UserLoginID("@alice:example.com")
	dm := &bridgev2.Portal{Portal: &database.Portal{
		ID:       MakePortalID("+15551234567@cheogram.com"),
		Receiver: login,
	}}
	e164, ok := x.isCallPortal(dm, login)
	if !ok || e164 != "+15551234567" {
		t.Fatalf("expected a call portal for the DM, got (%q, %v)", e164, ok)
	}
	if _, ok := x.isCallPortal(dm, "@bob:example.com"); ok {
		t.Fatal("a portal of another login must not be a call portal")
	}
	group := &bridgev2.Portal{Portal: &database.Portal{
		ID:       MakePortalID("+15551234567,+15551234568@cheogram.com"),
		Receiver: login,
	}}
	if _, ok := x.isCallPortal(group, login); ok {
		t.Fatal("a group text portal must not be a call portal")
	}
	short := &bridgev2.Portal{Portal: &database.Portal{
		ID:       MakePortalID("69061@cheogram.com"),
		Receiver: login,
	}}
	if _, ok := x.isCallPortal(short, login); ok {
		t.Fatal("a short code must not be a call portal")
	}
	if _, ok := x.isCallPortal(nil, login); ok {
		t.Fatal("nil portal must not be a call portal")
	}
}

func TestRegistrationStateStoppedWhenNil(t *testing.T) {
	t.Parallel()
	if got := registrationState(nil); got != "stopped" {
		t.Fatalf("expected stopped for a nil account, got %q", got)
	}
}

func TestDecideCallRoom(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name            string
		portalEncrypted bool
		callRoom        id.RoomID
		want            callRoomDecision
	}{
		{name: "plain portal", want: callRoomUsePortal},
		{name: "encrypted portal refused", portalEncrypted: true, want: callRoomRefuse},
		{name: "linked call room wins", callRoom: "!call:example.com", want: callRoomUseCallRoom},
		{name: "linked call room wins even for plain portal", callRoom: "!call:example.com", want: callRoomUseCallRoom},
		{name: "linked call room wins for encrypted portal", portalEncrypted: true, callRoom: "!call:example.com", want: callRoomUseCallRoom},
	}
	for _, tc := range cases {
		if got := decideCallRoom(tc.portalEncrypted, tc.callRoom); got != tc.want {
			t.Fatalf("%s: expected %v, got %v", tc.name, tc.want, got)
		}
	}
}

func TestCallRoomNameAndTopic(t *testing.T) {
	t.Parallel()
	if got := callRoomName("Alice O'Brien", "+15551234567"); got != "📞 Alice O'Brien (+15551234567)" {
		t.Fatalf("unexpected call room name: %q", got)
	}
	if got := callRoomName("", "+15551234567"); got != "📞 +15551234567" {
		t.Fatalf("unexpected call room name for empty display name: %q", got)
	}
	if got := callRoomName("+15551234567", "+15551234567"); got != "📞 +15551234567" {
		t.Fatalf("number used as display name must not be duplicated: %q", got)
	}
	if got := callRoomName("  ", "+15551234567"); got != "📞 +15551234567" {
		t.Fatalf("blank display name must fall back: %q", got)
	}
	topic := callRoomTopic("+15551234567")
	if !strings.Contains(topic, "+15551234567") || !strings.Contains(topic, "unencrypted") {
		t.Fatalf("unexpected call room topic: %q", topic)
	}
}

func TestParseCallRoomArgs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		args   []string
		target string
		remove bool
		force  bool
		ok     bool
	}{
		{name: "no args lists", ok: true},
		{name: "number", args: []string{"+15551234567"}, target: "+15551234567", ok: true},
		{name: "remove flag", args: []string{"--remove", "+15551234567"}, target: "+15551234567", remove: true, ok: true},
		{name: "remove verb", args: []string{"remove", "+15551234567"}, target: "+15551234567", remove: true, ok: true},
		{name: "force flag", args: []string{"--force", "+15551234567"}, target: "+15551234567", force: true, ok: true},
		{name: "flags after number", args: []string{"+15551234567", "--force"}, target: "+15551234567", force: true, ok: true},
		{name: "remove without number", args: []string{"--remove"}, ok: false},
		{name: "two numbers", args: []string{"+15551234567", "+15559876543"}, ok: false},
		{name: "empty arg ignored", args: []string{"", "+15551234567"}, target: "+15551234567", ok: true},
	}
	for _, tc := range cases {
		target, remove, force, ok := parseCallRoomArgs(tc.args)
		if ok != tc.ok || target != tc.target || remove != tc.remove || force != tc.force {
			t.Fatalf("%s: got (target=%q, remove=%v, force=%v, ok=%v), want (target=%q, remove=%v, force=%v, ok=%v)",
				tc.name, target, remove, force, ok, tc.target, tc.remove, tc.force, tc.ok)
		}
	}
	// A remove plus force combination is still accepted (force is simply unused).
	if _, remove, _, ok := parseCallRoomArgs([]string{"--remove", "--force", "+15551234567"}); !ok || !remove {
		t.Fatal("remove with force must be accepted")
	}
}

func TestPortalCallRoomID(t *testing.T) {
	t.Parallel()
	if got := portalCallRoomID(nil); got != "" {
		t.Fatalf("nil portal must have no call room, got %q", got)
	}
	plain := &bridgev2.Portal{Portal: &database.Portal{ID: networkid.PortalID("+15551234567@cheogram.com")}}
	if got := portalCallRoomID(plain); got != "" {
		t.Fatalf("portal without metadata must have no call room, got %q", got)
	}
	linked := &bridgev2.Portal{Portal: &database.Portal{ID: networkid.PortalID("+15551234567@cheogram.com")}}
	linked.Metadata = &PortalMetadata{CallRoomMXID: "!call:example.com"}
	if got := portalCallRoomID(linked); got != "!call:example.com" {
		t.Fatalf("unexpected call room id: %q", got)
	}
}

func TestIsCallPortalEmptyReceiver(t *testing.T) {
	t.Parallel()
	portal := &bridgev2.Portal{Portal: &database.Portal{ID: networkid.PortalID(MakePortalID("+15551234567@cheogram.com"))}}
	// database.Portal.Receiver may be empty when the bridge does not split portals.
	if e164, ok := (&callsExt{}).isCallPortal(portal, ""); !ok || e164 != "+15551234567" {
		t.Fatalf("empty receiver must be accepted, got (%q, %v)", e164, ok)
	}
	portal.Receiver = "login-1"
	if _, ok := (&callsExt{}).isCallPortal(portal, "login-2"); ok {
		t.Fatal("a portal belonging to another login must be rejected")
	}
	if _, ok := (&callsExt{}).isCallPortal(portal, "login-1"); !ok {
		t.Fatal("matching receiver must be accepted")
	}
}

func TestCallsConfigWidgetToHostkit(t *testing.T) {
	t.Parallel()
	notify := false
	cfg := CallsConfig{Widget: CallsWidgetConfig{
		Enabled:            true,
		PublicURL:          "https://widget.example.com",
		Secret:             "0123456789abcdef0123456789abcdef",
		FrameAncestors:     []string{"https://element.example.com"},
		AllowRemoteUsers:   true,
		RemoveWhenDisabled: true,
		TrustedProxies:     []string{"10.0.0.0/8"},
		PWA: CallsWidgetPWAConfig{
			Enabled:              true,
			InactivityDays:       30,
			MaxLifetimeDays:      365,
			RotationGraceSeconds: 60,
			EnrollMinutes:        10,
			MaxLinksPerUser:      3,
			NotifyOnEnroll:       &notify,
		},
	}}
	hk := cfg.toHostkit()
	if !hk.Widget.Enabled || hk.Widget.PublicURL != "https://widget.example.com" || hk.Widget.Secret != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("widget basic fields not mapped: %+v", hk.Widget)
	}
	if len(hk.Widget.FrameAncestors) != 1 || hk.Widget.FrameAncestors[0] != "https://element.example.com" {
		t.Fatalf("frame ancestors not mapped: %+v", hk.Widget.FrameAncestors)
	}
	if !hk.Widget.AllowRemoteUsers || !hk.Widget.RemoveWhenDisabled {
		t.Fatalf("widget booleans not mapped: %+v", hk.Widget)
	}
	if len(hk.Widget.TrustedProxies) != 1 || hk.Widget.TrustedProxies[0] != "10.0.0.0/8" {
		t.Fatalf("trusted proxies not mapped: %+v", hk.Widget.TrustedProxies)
	}
	pwa := hk.Widget.PWA
	if !pwa.Enabled || pwa.InactivityDays != 30 || pwa.MaxLifetimeDays != 365 || pwa.RotationGraceSeconds != 60 || pwa.EnrollMinutes != 10 || pwa.MaxLinksPerUser != 3 {
		t.Fatalf("PWA fields not mapped: %+v", pwa)
	}
	if pwa.NotifyOnEnroll == nil || *pwa.NotifyOnEnroll != false {
		t.Fatalf("PWA NotifyOnEnroll not mapped: %+v", pwa.NotifyOnEnroll)
	}
}

func TestCallsConfigAutoCreateCallRoomDefault(t *testing.T) {
	t.Parallel()
	if (CallsConfig{}).IsAutoCreateCallRoom() == false {
		t.Fatal("AutoCreateCallRoom should default to enabled")
	}
	if (CallsConfig{AutoCreateCallRoom: ptr.Ptr(false)}).IsAutoCreateCallRoom() {
		t.Fatal("explicit false should disable auto-creation")
	}
	if !(CallsConfig{AutoCreateCallRoom: ptr.Ptr(true)}).IsAutoCreateCallRoom() {
		t.Fatal("explicit true should enable auto-creation")
	}
}
