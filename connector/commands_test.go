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

func TestNormalizeJID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{input: "", want: ""},
		{input: "   ", want: ""},
		{input: "alice@example.com", want: "alice@example.com"},
		{input: "  bob@example.com/phone  ", want: "bob@example.com/phone"},
		{input: "+15551234567", want: "+15551234567@cheogram.com"},
		{input: "15551234567", want: "+15551234567@cheogram.com"},
		{input: "+1 (555) 123-4567", want: "+15551234567@cheogram.com"},
		{input: "555-123-4567", want: "+5551234567@cheogram.com"},
		{input: "555.123.4567", want: "+5551234567@cheogram.com"},
		{input: "(555) 123 4567", want: "+5551234567@cheogram.com"},
		{input: "invalid_no_digits!", want: ""},
	}

	for _, tt := range tests {
		if got := normalizeJID(tt.input); got != tt.want {
			t.Errorf("normalizeJID(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestParsePruneRoomsArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		args          []string
		wantConfirm   bool
		wantEmptyOnly bool
		wantDays      int
		wantErr       bool
	}{
		{args: []string{}, wantConfirm: false, wantEmptyOnly: false, wantDays: 30, wantErr: false},
		{args: []string{"dry-run"}, wantConfirm: false, wantEmptyOnly: false, wantDays: 30, wantErr: false},
		{args: []string{"--dry-run"}, wantConfirm: false, wantEmptyOnly: false, wantDays: 30, wantErr: false},
		{args: []string{"dry-run", "60"}, wantConfirm: false, wantEmptyOnly: false, wantDays: 60, wantErr: false},
		{args: []string{"14"}, wantConfirm: false, wantEmptyOnly: false, wantDays: 14, wantErr: false},
		{args: []string{"confirm"}, wantConfirm: true, wantEmptyOnly: false, wantDays: 30, wantErr: false},
		{args: []string{"yes"}, wantConfirm: true, wantEmptyOnly: false, wantDays: 30, wantErr: false},
		{args: []string{"--yes"}, wantConfirm: true, wantEmptyOnly: false, wantDays: 30, wantErr: false},
		{args: []string{"confirm", "7"}, wantConfirm: true, wantEmptyOnly: false, wantDays: 7, wantErr: false},
		{args: []string{"yes", "90"}, wantConfirm: true, wantEmptyOnly: false, wantDays: 90, wantErr: false},
		{args: []string{"empty"}, wantConfirm: false, wantEmptyOnly: true, wantDays: 30, wantErr: false},
		{args: []string{"--empty"}, wantConfirm: false, wantEmptyOnly: true, wantDays: 30, wantErr: false},
		{args: []string{"empty-only"}, wantConfirm: false, wantEmptyOnly: true, wantDays: 30, wantErr: false},
		{args: []string{"empty", "confirm"}, wantConfirm: true, wantEmptyOnly: true, wantDays: 30, wantErr: false},
		{args: []string{"confirm", "empty"}, wantConfirm: true, wantEmptyOnly: true, wantDays: 30, wantErr: false},
		{args: []string{"0"}, wantConfirm: false, wantEmptyOnly: true, wantDays: 0, wantErr: false},
		{args: []string{"confirm", "0"}, wantConfirm: true, wantEmptyOnly: true, wantDays: 0, wantErr: false},
		{args: []string{"-5"}, wantErr: true},
		{args: []string{"invalid_arg"}, wantErr: true},
	}

	for _, tt := range tests {
		confirm, emptyOnly, days, err := parsePruneRoomsArgs(tt.args)
		if (err != nil) != tt.wantErr {
			t.Errorf("parsePruneRoomsArgs(%v) error = %v, wantErr = %v", tt.args, err, tt.wantErr)
			continue
		}
		if !tt.wantErr {
			if confirm != tt.wantConfirm {
				t.Errorf("parsePruneRoomsArgs(%v) confirm = %v, want %v", tt.args, confirm, tt.wantConfirm)
			}
			if emptyOnly != tt.wantEmptyOnly {
				t.Errorf("parsePruneRoomsArgs(%v) emptyOnly = %v, want %v", tt.args, emptyOnly, tt.wantEmptyOnly)
			}
			if days != tt.wantDays {
				t.Errorf("parsePruneRoomsArgs(%v) days = %v, want %v", tt.args, days, tt.wantDays)
			}
		}
	}
}

func TestParsePruneDuplicatesArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		args        []string
		wantConfirm bool
		wantErr     bool
	}{
		{args: []string{}, wantConfirm: false, wantErr: false},
		{args: []string{"dry-run"}, wantConfirm: false, wantErr: false},
		{args: []string{"--dry-run"}, wantConfirm: false, wantErr: false},
		{args: []string{"confirm"}, wantConfirm: true, wantErr: false},
		{args: []string{"yes"}, wantConfirm: true, wantErr: false},
		{args: []string{"--yes"}, wantConfirm: true, wantErr: false},
		{args: []string{"invalid"}, wantErr: true},
	}

	for _, tt := range tests {
		confirm, err := parsePruneDuplicatesArgs(tt.args)
		if (err != nil) != tt.wantErr {
			t.Errorf("parsePruneDuplicatesArgs(%v) error = %v, wantErr = %v", tt.args, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && confirm != tt.wantConfirm {
			t.Errorf("parsePruneDuplicatesArgs(%v) confirm = %v, want %v", tt.args, confirm, tt.wantConfirm)
		}
	}
}

func TestFormatLastActivity(t *testing.T) {
	t.Parallel()

	if got := formatLastActivity(time.Time{}); got != "Never" {
		t.Errorf("formatLastActivity(zero) = %q, want %q", got, "Never")
	}

	now := time.Now()
	if got := formatLastActivity(now); !strings.HasPrefix(got, "Today") {
		t.Errorf("formatLastActivity(now) = %q, want prefix 'Today'", got)
	}

	yesterday := now.Add(-25 * time.Hour)
	if got := formatLastActivity(yesterday); !strings.HasPrefix(got, "Yesterday") {
		t.Errorf("formatLastActivity(yesterday) = %q, want prefix 'Yesterday'", got)
	}

	old := now.Add(-40 * 24 * time.Hour)
	if got := formatLastActivity(old); !strings.Contains(got, "40 days ago") {
		t.Errorf("formatLastActivity(40d ago) = %q, want '40 days ago'", got)
	}
}
