// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"gopkg.in/yaml.v3"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/id"
)

func mustCompileTTLPatterns(t *testing.T, names []string, custom map[string]string) []ttlRedactPattern {
	t.Helper()
	patterns, err := compileTTLPatterns(names, custom)
	if err != nil {
		t.Fatalf("compileTTLPatterns(%v, %v) error: %v", names, custom, err)
	}
	return patterns
}

func unmarshalTTLNetworkConfig(t *testing.T, content string) *SimpleConfig {
	t.Helper()
	var wrapper struct {
		Network *SimpleConfig `yaml:"network"`
	}
	if err := yaml.Unmarshal([]byte(content), &wrapper); err != nil {
		t.Fatalf("failed to unmarshal network config: %v", err)
	}
	if wrapper.Network == nil {
		t.Fatal("network config was not parsed")
	}
	return wrapper.Network
}

func TestParseTTLDuration(t *testing.T) {
	tests := []struct {
		value   string
		want    time.Duration
		wantErr bool
	}{
		{value: "", want: 0},
		{value: "0", want: 0},
		{value: "off", want: 0},
		{value: "never", want: 0},
		{value: "12h", want: 12 * time.Hour},
		{value: "30d", want: 30 * 24 * time.Hour},
		{value: "365d", want: 365 * 24 * time.Hour},
		{value: "nope", wantErr: true},
		{value: "-1h", wantErr: true},
	}
	for _, test := range tests {
		got, err := parseTTLDuration(test.value)
		if test.wantErr {
			if err == nil {
				t.Errorf("parseTTLDuration(%q) expected an error", test.value)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseTTLDuration(%q) error: %v", test.value, err)
			continue
		}
		if got != test.want {
			t.Errorf("parseTTLDuration(%q) = %s, want %s", test.value, got, test.want)
		}
	}
}

func TestTTLPatternPresets(t *testing.T) {
	tests := []struct {
		preset   string
		body     string
		wantName string
	}{
		{preset: "otp", body: "Your verification code is 123456", wantName: "otp"},
		{preset: "otp", body: "1234 is your OTP", wantName: "otp"},
		{preset: "otp", body: "Passcode: 9988", wantName: "otp"},
		{preset: "otp", body: "Meet me at 8, bring 1234 apples"},
		{preset: "credit-card", body: "card 4111 1111 1111 1111 expires soon", wantName: "credit-card"},
		{preset: "credit-card", body: "4111111111111111", wantName: "credit-card"},
		{preset: "credit-card", body: "call me at 15550100100"},
		{preset: "ssn", body: "SSN 123-45-6789", wantName: "ssn"},
		{preset: "ssn", body: "order 123456789"},
	}
	for _, test := range tests {
		patterns := mustCompileTTLPatterns(t, []string{test.preset}, nil)
		got := matchTTLPattern(patterns, test.body)
		if got != test.wantName {
			t.Errorf("matchTTLPattern(%s, %q) = %q, want %q", test.preset, test.body, got, test.wantName)
		}
	}
	if matchTTLPattern(mustCompileTTLPatterns(t, []string{"otp"}, nil), "") != "" {
		t.Error("empty body must not match")
	}
}

func TestCompileTTLPatterns(t *testing.T) {
	if patterns, err := compileTTLPatterns(nil, nil); err != nil || patterns != nil {
		t.Errorf("compileTTLPatterns(nil, nil) = %v, %v; want nil, nil", patterns, err)
	}
	patterns := mustCompileTTLPatterns(t, []string{"otp"}, map[string]string{"zeta": `z+`, "alpha": `a+`})
	if len(patterns) != 3 {
		t.Fatalf("pattern count = %d, want 3", len(patterns))
	}
	if patterns[0].Name != "otp" || patterns[1].Name != "alpha" || patterns[2].Name != "zeta" {
		t.Errorf("pattern order = %s, %s, %s; want otp, alpha, zeta", patterns[0].Name, patterns[1].Name, patterns[2].Name)
	}
	if _, err := compileTTLPatterns([]string{"does-not-exist"}, nil); err == nil {
		t.Error("unknown preset must be rejected")
	}
	if _, err := compileTTLPatterns(nil, map[string]string{"bad": "("}); err == nil {
		t.Error("invalid custom pattern must be rejected")
	}
}

func TestResolveTTLFor(t *testing.T) {
	policy := ttlPolicy{
		Global:  30 * 24 * time.Hour,
		PerRoom: map[string]time.Duration{"!room:example.com": 7 * 24 * time.Hour},
		PerContact: map[string]time.Duration{
			"+15550100100@cheogram.com": 24 * time.Hour,
			"+15551111111@cheogram.com": 0,
		},
	}
	tests := []struct {
		name    string
		roomID  id.RoomID
		contact string
		wantTTL time.Duration
		wantOK  bool
	}{
		{name: "contact wins", roomID: "!room:example.com", contact: "+15550100100@cheogram.com", wantTTL: 24 * time.Hour, wantOK: true},
		{name: "room override", roomID: "!room:example.com", contact: "+15551234567@cheogram.com", wantTTL: 7 * 24 * time.Hour, wantOK: true},
		{name: "global fallback", roomID: "!other:example.com", contact: "+15551234567@cheogram.com", wantTTL: 30 * 24 * time.Hour, wantOK: true},
		{name: "explicit keep forever", roomID: "!other:example.com", contact: "+15551111111@cheogram.com", wantTTL: 0, wantOK: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ttl, ok := resolveTTLFor(policy, test.roomID, test.contact)
			if ttl != test.wantTTL || ok != test.wantOK {
				t.Errorf("resolveTTLFor(%s, %s) = %s, %t; want %s, %t", test.roomID, test.contact, ttl, ok, test.wantTTL, test.wantOK)
			}
		})
	}
	if _, ok := resolveTTLFor(ttlPolicy{}, "!room:example.com", "+15551234567@cheogram.com"); ok {
		t.Error("empty policy must not resolve a TTL")
	}
}

func TestPlanTTLRedactions(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	policy := ttlPolicy{
		Global:  30 * 24 * time.Hour,
		PerRoom: map[string]time.Duration{"!room:example.com": 7 * 24 * time.Hour},
		PerContact: map[string]time.Duration{
			"+15550000000@cheogram.com": 24 * time.Hour,
			"+15551111111@cheogram.com": 0,
		},
		Patterns: mustCompileTTLPatterns(t, []string{"otp"}, nil),
	}
	candidates := []ttlCandidate{
		{RowID: 1, MatrixRoom: "!other:example.com", Contact: "+15550000000@cheogram.com", Timestamp: now.Add(-48 * time.Hour)},
		{RowID: 2, MatrixRoom: "!room:example.com", Contact: "+15551234567@cheogram.com", Timestamp: now.Add(-10 * 24 * time.Hour)},
		{RowID: 3, MatrixRoom: "!other:example.com", Contact: "+15551234567@cheogram.com", Timestamp: now.Add(-1 * time.Hour)},
		{RowID: 4, MatrixRoom: "!other:example.com", Contact: "+15551111111@cheogram.com", Timestamp: now.Add(-365 * 24 * time.Hour)},
		{RowID: 5, MatrixRoom: "!other:example.com", Contact: "+15551234567@cheogram.com", Timestamp: now.Add(-1 * time.Hour), Body: "Your verification code is 123456", HasMedia: true},
		{RowID: 6, MatrixRoom: "!other:example.com", Contact: "+15551234567@cheogram.com", Timestamp: now.Add(-40 * 24 * time.Hour), HasMedia: true},
	}

	decisions := planTTLRedactions(policy, candidates, now)
	if len(decisions) != 3 {
		t.Fatalf("decision count = %d, want 3 (%+v)", len(decisions), decisions)
	}
	byRowID := make(map[int64]string, len(decisions))
	for _, decision := range decisions {
		byRowID[decision.Candidate.RowID] = decision.Reason
	}
	if byRowID[1] != "ttl" {
		t.Errorf("contact override decision = %q, want ttl", byRowID[1])
	}
	if byRowID[2] != "ttl" {
		t.Errorf("room override decision = %q, want ttl", byRowID[2])
	}
	if byRowID[5] != "pattern:otp" {
		t.Errorf("pattern decision = %q, want pattern:otp", byRowID[5])
	}
	if _, ok := byRowID[3]; ok {
		t.Error("recent message inside the global window must be kept")
	}
	if _, ok := byRowID[4]; ok {
		t.Error("explicit keep-forever contact must be kept")
	}
	if _, ok := byRowID[6]; ok {
		t.Error("media message must be kept when delete_media is false")
	}

	withMedia := policy
	withMedia.DeleteMedia = true
	decisions = planTTLRedactions(withMedia, candidates, now)
	if len(decisions) != 4 {
		t.Fatalf("decision count with media = %d, want 4 (%+v)", len(decisions), decisions)
	}
	foundMedia := false
	for _, decision := range decisions {
		if decision.Candidate.RowID == 6 {
			foundMedia = true
		}
	}
	if !foundMedia {
		t.Error("media message must be redacted when delete_media is true")
	}
}

func TestPlanTTLRedactionsPatternOnly(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	policy := ttlPolicy{Patterns: mustCompileTTLPatterns(t, []string{"otp"}, nil)}
	candidates := []ttlCandidate{
		{RowID: 1, Timestamp: now, Body: "Your verification code is 123456"},
		{RowID: 2, Timestamp: now.Add(-1000 * time.Hour), Body: "hello"},
	}
	decisions := planTTLRedactions(policy, candidates, now)
	if len(decisions) != 1 || decisions[0].Candidate.RowID != 1 {
		t.Fatalf("pattern-only decisions = %+v, want only the matching message", decisions)
	}
}

func TestTTLContactForPortal(t *testing.T) {
	dm := &bridgev2.Portal{Portal: &database.Portal{
		PortalKey:   networkid.PortalKey{ID: "thread-1-1"},
		OtherUserID: MakeUserID("+15550100100"),
	}}
	if got := ttlContactForPortal(dm); got != "+15550100100@cheogram.com" {
		t.Errorf("DM contact = %q, want +15550100100@cheogram.com", got)
	}

	jidDM := &bridgev2.Portal{Portal: &database.Portal{
		PortalKey:   networkid.PortalKey{ID: "alice@example.com"},
		OtherUserID: MakeUserID("alice@example.com"),
	}}
	if got := ttlContactForPortal(jidDM); got != "alice@example.com" {
		t.Errorf("JID DM contact = %q, want alice@example.com", got)
	}

	group := &bridgev2.Portal{Portal: &database.Portal{
		PortalKey: networkid.PortalKey{ID: MakePortalID("+15550100100,+15551234567@cheogram.com")},
	}}
	if got := ttlContactForPortal(group); got != "" {
		t.Errorf("group text contact = %q, want empty", got)
	}

	muc := &bridgev2.Portal{Portal: &database.Portal{
		PortalKey:   networkid.PortalKey{ID: MakePortalID("room@muc.example.com")},
		OtherUserID: MakeUserID("+15550100100"),
		Metadata:    &PortalMetadata{IsMUC: true},
	}}
	if got := ttlContactForPortal(muc); got != "" {
		t.Errorf("MUC contact = %q, want empty", got)
	}

	if got := ttlContactForPortal(nil); got != "" {
		t.Errorf("nil portal contact = %q, want empty", got)
	}
}

func TestHistoryTTLConfigDefaults(t *testing.T) {
	cfg := unmarshalTTLNetworkConfig(t, "network: {}\n")
	ttl := cfg.HistoryTTL
	if ttl.Enabled {
		t.Error("history TTL should be disabled by default")
	}
	if !ttl.DeleteMedia {
		t.Error("delete_media should default to true")
	}
	if ttl.Interval.Duration() != defaultHistoryTTLInterval {
		t.Errorf("interval = %s, want %s", ttl.Interval.Duration(), defaultHistoryTTLInterval)
	}
	if ttl.ScanWindow.Duration() != defaultHistoryTTLScanWindow {
		t.Errorf("scan window = %s, want %s", ttl.ScanWindow.Duration(), defaultHistoryTTLScanWindow)
	}
	if ttl.ScanLimit != defaultHistoryTTLScanLimit {
		t.Errorf("scan limit = %d, want %d", ttl.ScanLimit, defaultHistoryTTLScanLimit)
	}
	if cfg.ContactNameSuffix != defaultContactNameSuffix {
		t.Errorf("contact name suffix = %q, want %q", cfg.ContactNameSuffix, defaultContactNameSuffix)
	}
}

func TestHistoryTTLConfigPartialMapping(t *testing.T) {
	cfg := unmarshalTTLNetworkConfig(t, `
network:
  contact_name_suffix: test
  history_days: 7
  history_ttl:
    enabled: true
    global: 30d
    per_room:
      "!room:example.com": 7d
    per_contact:
      "+1 (555) 010-0100": 0
      "alice@Example.COM/resource": 12h
    patterns: [otp, credit-card]
    custom_patterns:
      ticket: 'TICKET-\d+'
    delete_media: false
    interval: 12h
    scan_window: 14d
    scan_limit: 100
    dry_run: true
`)
	ttl := cfg.HistoryTTL
	if !ttl.Enabled || !ttl.DryRun {
		t.Error("enabled/dry_run flags were not read")
	}
	if ttl.Global.Duration() != 30*24*time.Hour {
		t.Errorf("global = %s, want 30d", ttl.Global.Duration())
	}
	if ttl.PerRoom["!room:example.com"].Duration() != 7*24*time.Hour {
		t.Errorf("per_room = %+v", ttl.PerRoom)
	}
	if len(ttl.PerContact) != 2 ||
		ttl.PerContact["+15550100100@cheogram.com"].Duration() != 0 ||
		ttl.PerContact["alice@example.com"].Duration() != 12*time.Hour {
		t.Errorf("per_contact was not canonicalized: %+v", ttl.PerContact)
	}
	if len(ttl.Patterns) != 2 {
		t.Errorf("patterns = %v", ttl.Patterns)
	}
	if len(ttl.CustomPatterns) != 1 || ttl.CustomPatterns["ticket"] != `TICKET-\d+` {
		t.Errorf("custom patterns = %+v", ttl.CustomPatterns)
	}
	if ttl.DeleteMedia {
		t.Error("delete_media: false was not applied")
	}
	if ttl.Interval.Duration() != 12*time.Hour || ttl.ScanWindow.Duration() != 14*24*time.Hour || ttl.ScanLimit != 100 {
		t.Errorf("interval/window/limit = %s/%s/%d", ttl.Interval.Duration(), ttl.ScanWindow.Duration(), ttl.ScanLimit)
	}
	if cfg.ContactNameSuffix != "test" || cfg.HistoryDays != 7 {
		t.Errorf("sibling config keys were not applied: %+v", cfg)
	}
}

func TestHistoryTTLConfigRejectsBadPatterns(t *testing.T) {
	tests := []string{
		"network:\n  history_ttl:\n    patterns: [nope]\n",
		"network:\n  history_ttl:\n    custom_patterns:\n      bad: '('\n",
		"network:\n  history_ttl:\n    scan_limit: -1\n",
		"network:\n  history_ttl:\n    global: soon\n",
		"network:\n  history_ttl:\n    interval: 12x\n",
	}
	for _, content := range tests {
		var wrapper struct {
			Network *SimpleConfig `yaml:"network"`
		}
		if err := yaml.Unmarshal([]byte(content), &wrapper); err == nil {
			t.Errorf("expected an error for config:\n%s", content)
		}
	}
}

func TestHistoryTTLConfigMarshalRoundTrip(t *testing.T) {
	cfg := unmarshalTTLNetworkConfig(t, `
network:
  history_ttl:
    enabled: true
    global: 30d
    per_contact:
      "+15550100100": 7d
    patterns: [otp]
    delete_media: false
    interval: 12h
`).HistoryTTL
	data, err := yaml.Marshal(&cfg)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	var back HistoryTTLConfig
	if err := yaml.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal error: %v (data: %s)", err, data)
	}
	if back.Global.Duration() != cfg.Global.Duration() ||
		back.Interval.Duration() != cfg.Interval.Duration() ||
		back.PerContact["+15550100100@cheogram.com"].Duration() != 7*24*time.Hour ||
		back.DeleteMedia ||
		len(back.Patterns) != 1 || back.Patterns[0] != "otp" ||
		!back.Enabled {
		t.Errorf("round trip mismatch:\nwant %+v\ngot  %+v\n%s", cfg, back, data)
	}
	if !strings.Contains(string(data), "30d") {
		t.Errorf("durations should marshal as day strings:\n%s", data)
	}
}

func TestHistoryTTLConfigScalarFileDefaults(t *testing.T) {
	cfg := unmarshalTTLNetworkConfig(t, "network: test\n")
	if cfg.ContactNameSuffix != "test" {
		t.Errorf("contact name suffix = %q, want test", cfg.ContactNameSuffix)
	}
	if cfg.HistoryTTL.Interval.Duration() != defaultHistoryTTLInterval || !cfg.HistoryTTL.DeleteMedia {
		t.Errorf("history TTL defaults were lost in the scalar branch: %+v", cfg.HistoryTTL)
	}
}

func TestHistoryTTLConfigFromScalarFilePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "simple-config.yaml")
	body := "history_days: 7\nhistory_ttl:\n  enabled: true\n  global: 30d\n  patterns: [otp]\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}
	cfg := unmarshalTTLNetworkConfig(t, "network: "+path+"\n")
	if !cfg.HistoryTTL.Enabled || cfg.HistoryTTL.Global.Duration() != 30*24*time.Hour {
		t.Errorf("history TTL was not read from the referenced file: %+v", cfg.HistoryTTL)
	}
	if len(cfg.HistoryTTL.Patterns) != 1 || cfg.HistoryTTL.Patterns[0] != "otp" {
		t.Errorf("history TTL patterns = %v, want [otp]", cfg.HistoryTTL.Patterns)
	}
	if cfg.HistoryTTL.Interval.Duration() != defaultHistoryTTLInterval {
		t.Errorf("history TTL interval = %s, want the default %s", cfg.HistoryTTL.Interval.Duration(), defaultHistoryTTLInterval)
	}
}

func TestFormatTTLStatusDefaults(t *testing.T) {
	connector := &MyConnector{log: zerolog.Nop()}
	status := connector.formatTTLStatus()
	for _, want := range []string{
		"History TTL: disabled",
		"Global retention: none",
		"Overrides: 0 room(s), 0 contact(s)",
		"Patterns: none (retention only)",
		"Delete media: true",
		"Last run: never",
	} {
		if !strings.Contains(status, want) {
			t.Errorf("formatTTLStatus() missing %q:\n%s", want, status)
		}
	}
}

func TestFormatTTLRunResult(t *testing.T) {
	got := formatTTLRunResult(ttlRunResult{Portals: 2, Messages: 5, Redacted: 3, Errors: 1}, true)
	want := "History TTL dry run finished: 2 portal(s) scanned, 5 message(s) inspected, 3 redaction(s), 1 error(s)."
	if got != want {
		t.Errorf("formatTTLRunResult() = %q, want %q", got, want)
	}
	if got := formatTTLPatterns([]string{"otp"}, map[string]string{"ticket": `T+`}); got != "otp, 1 custom (ticket)" {
		t.Errorf("formatTTLPatterns() = %q", got)
	}
}
