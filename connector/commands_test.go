package connector

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
)

func TestSplitCheogramGroupContacts(t *testing.T) {
	t.Parallel()

	got := splitCheogramGroupContacts("+15551230001,+15551230002@cheogram.com")
	want := []string{"+15551230001@cheogram.com", "+15551230002@cheogram.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("splitCheogramGroupContacts() = %v, want %v", got, want)
	}
}

func TestAddContactedJIDs(t *testing.T) {
	t.Parallel()

	contacted := map[string]struct{}{}
	for _, jid := range splitCheogramGroupContacts("+15551230001,+15551230002@cheogram.com") {
		addContactedJIDs(contacted, jid)
	}
	addContactedJIDs(contacted, "+15551230003@cheogram.com")

	got := make([]string, 0, len(contacted))
	for jid := range contacted {
		got = append(got, jid)
	}
	want := map[string]struct{}{
		"+15551230001@cheogram.com": {},
		"+15551230002@cheogram.com": {},
		"+15551230003@cheogram.com": {},
	}
	if len(got) != len(want) {
		t.Fatalf("addContactedJIDs() size = %d, want %d", len(got), len(want))
	}
	for jid := range want {
		if _, ok := contacted[jid]; !ok {
			t.Fatalf("missing contacted jid %s", jid)
		}
	}
}

func TestParseBlockCommandArgs(t *testing.T) {
	t.Parallel()

	jid, report, err := parseBlockCommandArgs([]string{"--report", "+15551230001"}, false)
	if err != nil {
		t.Fatalf("parseBlockCommandArgs() returned error: %v", err)
	}
	if jid != "+15551230001@cheogram.com" {
		t.Fatalf("jid = %q, want %q", jid, "+15551230001@cheogram.com")
	}
	if !report {
		t.Fatal("expected report=true")
	}

	jid, report, err = parseBlockCommandArgs([]string{"+15551230002@cheogram.com"}, true)
	if err != nil {
		t.Fatalf("parseBlockCommandArgs() with default report returned error: %v", err)
	}
	if jid != "+15551230002@cheogram.com" {
		t.Fatalf("jid = %q, want %q", jid, "+15551230002@cheogram.com")
	}
	if !report {
		t.Fatal("expected default report=true")
	}
}

func TestFormatPingStatusConnected(t *testing.T) {
	t.Parallel()

	login := &bridgev2.UserLogin{UserLogin: &database.UserLogin{Metadata: &LoginMetadata{RemoteUserID: "user@cheogram.com"}}}
	state := loginBridgeStateSummaryResult{State: "connected", Timestamp: "2026-01-02T03:04:05Z"}
	conn := loginConnectionStatus{
		Connected:          true,
		SessionEstablished: true,
		BindJID:            "user@cheogram.com/resource",
		Uptime:             2*time.Minute + 3*time.Second,
	}
	got := formatPingStatus(login, state, conn, xmppPingSummary{RTT: 42 * time.Millisecond})
	for _, want := range []string{
		"Remote: user@cheogram.com",
		"Bridge state: connected at 2026-01-02T03:04:05Z",
		"XMPP session: connected",
		"Bound JID: user@cheogram.com/resource",
		"Session uptime: 2m3s",
		"XMPP ping: ok (42ms)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatPingStatus() missing %q in:\n%s", want, got)
		}
	}
}

func TestFormatPingStatusDisconnected(t *testing.T) {
	t.Parallel()

	got := formatPingStatus(nil, loginBridgeStateSummaryResult{State: "unknown", Timestamp: "unknown"}, loginConnectionStatus{}, xmppPingSummary{Skipped: true, Detail: "not connected"})
	for _, want := range []string{
		"Login: (unknown)",
		"Remote: (unknown)",
		"XMPP session: disconnected",
		"XMPP ping: skipped (not connected)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatPingStatus() missing %q in:\n%s", want, got)
		}
	}
}

func TestServerDomainFromJID(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"user@example.com/resource": "example.com",
		"user@example.com":          "example.com",
		"example.com/resource":      "example.com",
		"example.com":               "example.com",
		"":                          "",
	}
	for input, want := range tests {
		if got := serverDomainFromJID(input); got != want {
			t.Fatalf("serverDomainFromJID(%q) = %q, want %q", input, got, want)
		}
	}
}
