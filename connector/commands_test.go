package connector

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/id"
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

func TestParseRebindRoomArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		args        []string
		wantContact string
		wantRoomID  string
		wantErr     bool
	}{
		{
			name:        "jid first then room ID",
			args:        []string{"+15551234567", "!oldroom:example.com"},
			wantContact: "+15551234567",
			wantRoomID:  "!oldroom:example.com",
		},
		{
			name:        "room ID first then phone",
			args:        []string{"!oldroom:example.com", "+15551234567"},
			wantContact: "+15551234567",
			wantRoomID:  "!oldroom:example.com",
		},
		{
			name:        "full jid and room ID",
			args:        []string{"contact@cheogram.com", "!oldroom:example.com"},
			wantContact: "contact@cheogram.com",
			wantRoomID:  "!oldroom:example.com",
		},
		{
			name:        "two room IDs",
			args:        []string{"!oldroom:example.com", "!newroom:example.com"},
			wantContact: "!oldroom:example.com",
			wantRoomID:  "!newroom:example.com",
		},
		{
			name:    "missing arguments",
			args:    []string{"!oldroom:example.com"},
			wantErr: true,
		},
		{
			name:    "too many arguments",
			args:    []string{"!oldroom:example.com", "+15551234567", "extra"},
			wantErr: true,
		},
		{
			name:    "no room id starting with !",
			args:    []string{"contact@cheogram.com", "+15551234567"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			contact, roomID, err := parseRebindRoomArgs(tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseRebindRoomArgs(%v) error = %v, wantErr = %v", tt.args, err, tt.wantErr)
			}
			if !tt.wantErr {
				if contact != tt.wantContact {
					t.Errorf("contact = %q, want %q", contact, tt.wantContact)
				}
				if string(roomID) != tt.wantRoomID {
					t.Errorf("roomID = %q, want %q", roomID, tt.wantRoomID)
				}
			}
		})
	}
}

func TestParseReplaceRoomArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		args          []string
		wantTarget    string
		wantDeleteOld bool
		wantErr       bool
	}{
		{
			name:          "target room ID only",
			args:          []string{"!room:example.com"},
			wantTarget:    "!room:example.com",
			wantDeleteOld: false,
		},
		{
			name:          "target phone with delete-old",
			args:          []string{"+15551234567", "delete-old"},
			wantTarget:    "+15551234567",
			wantDeleteOld: true,
		},
		{
			name:          "delete-old flag first",
			args:          []string{"--delete-old", "user@cheogram.com"},
			wantTarget:    "user@cheogram.com",
			wantDeleteOld: true,
		},
		{
			name:    "empty args",
			args:    []string{},
			wantErr: true,
		},
		{
			name:    "too many non-flag args",
			args:    []string{"!room1:example.com", "!room2:example.com"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target, deleteOld, err := parseReplaceRoomArgs(tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseReplaceRoomArgs(%v) error = %v, wantErr = %v", tt.args, err, tt.wantErr)
			}
			if !tt.wantErr {
				if target != tt.wantTarget {
					t.Errorf("target = %q, want %q", target, tt.wantTarget)
				}
				if deleteOld != tt.wantDeleteOld {
					t.Errorf("deleteOld = %v, want %v", deleteOld, tt.wantDeleteOld)
				}
			}
		})
	}
}

func TestParseReplaceAllArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		args          []string
		wantConfirm   bool
		wantDeleteOld bool
		wantErr       bool
	}{
		{
			name:          "empty args defaults to dry-run keep-old",
			args:          []string{},
			wantConfirm:   false,
			wantDeleteOld: false,
		},
		{
			name:          "dry-run flag",
			args:          []string{"dry-run"},
			wantConfirm:   false,
			wantDeleteOld: false,
		},
		{
			name:          "confirm flag",
			args:          []string{"confirm"},
			wantConfirm:   true,
			wantDeleteOld: false,
		},
		{
			name:          "confirm with delete-old",
			args:          []string{"delete-old", "confirm"},
			wantConfirm:   true,
			wantDeleteOld: true,
		},
		{
			name:          "yes with --delete-old",
			args:          []string{"--yes", "--delete-old"},
			wantConfirm:   true,
			wantDeleteOld: true,
		},
		{
			name:    "invalid argument",
			args:    []string{"unknown"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			confirm, deleteOld, err := parseReplaceAllArgs(tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseReplaceAllArgs(%v) error = %v, wantErr = %v", tt.args, err, tt.wantErr)
			}
			if !tt.wantErr {
				if confirm != tt.wantConfirm {
					t.Errorf("confirm = %v, want %v", confirm, tt.wantConfirm)
				}
				if deleteOld != tt.wantDeleteOld {
					t.Errorf("deleteOld = %v, want %v", deleteOld, tt.wantDeleteOld)
				}
			}
		})
	}
}

func TestSortRoomPruneStatsDesc(t *testing.T) {
	t.Parallel()

	now := time.Now()
	p1 := &bridgev2.Portal{Portal: &database.Portal{MXID: "!room1:example.com"}}
	p2 := &bridgev2.Portal{Portal: &database.Portal{MXID: "!room2:example.com"}}
	p3 := &bridgev2.Portal{Portal: &database.Portal{MXID: "!room3:example.com"}}

	stats := []*roomPruneStats{
		{portal: p1, messageCount: 0, lastTS: now.Add(-10 * time.Hour)},
		{portal: p2, messageCount: 50, lastTS: now.Add(-1 * time.Hour)},
		{portal: p3, messageCount: 10, lastTS: now},
	}

	sortRoomPruneStatsDesc(stats)

	if stats[0].portal.MXID != "!room2:example.com" {
		t.Errorf("stats[0] MXID = %s, want !room2:example.com", stats[0].portal.MXID)
	}
	if stats[1].portal.MXID != "!room3:example.com" {
		t.Errorf("stats[1] MXID = %s, want !room3:example.com", stats[1].portal.MXID)
	}
	if stats[2].portal.MXID != "!room1:example.com" {
		t.Errorf("stats[2] MXID = %s, want !room1:example.com", stats[2].portal.MXID)
	}
}

func TestFormatPortalName(t *testing.T) {
	t.Parallel()

	if name := formatPortalName(nil); name != "(unknown)" {
		t.Errorf("formatPortalName(nil) = %q, want %q", name, "(unknown)")
	}

	pWithName := &bridgev2.Portal{Portal: &database.Portal{Name: "Alice"}}
	if name := formatPortalName(pWithName); name != "Alice" {
		t.Errorf("formatPortalName(named) = %q, want %q", name, "Alice")
	}

	pWithID := &bridgev2.Portal{Portal: &database.Portal{PortalKey: networkid.PortalKey{ID: "=2b15551234567@cheogram.com"}}}
	if name := formatPortalName(pWithID); name != "+15551234567@cheogram.com" {
		t.Errorf("formatPortalName(unescaped) = %q, want %q", name, "+15551234567@cheogram.com")
	}
}

func TestParseRebindSpaceArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		args       []string
		wantRoomID id.RoomID
		wantErr    bool
	}{
		{
			name:       "valid space room ID",
			args:       []string{"!space1:example.com"},
			wantRoomID: "!space1:example.com",
		},
		{
			name:    "empty args",
			args:    []string{},
			wantErr: true,
		},
		{
			name:    "too many args",
			args:    []string{"!space1:example.com", "extra"},
			wantErr: true,
		},
		{
			name:    "invalid room ID without !",
			args:    []string{"space1:example.com"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			roomID, err := parseRebindSpaceArgs(tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseRebindSpaceArgs(%v) error = %v, wantErr = %v", tt.args, err, tt.wantErr)
			}
			if !tt.wantErr && roomID != tt.wantRoomID {
				t.Errorf("roomID = %s, want %s", roomID, tt.wantRoomID)
			}
		})
	}
}

func TestParseDeleteSpaceArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		args       []string
		wantRoomID id.RoomID
		wantForce  bool
		wantErr    bool
	}{
		{
			name:       "valid space room ID without force",
			args:       []string{"!space1:example.com"},
			wantRoomID: "!space1:example.com",
			wantForce:  false,
		},
		{
			name:       "valid space room ID with --force",
			args:       []string{"!space1:example.com", "--force"},
			wantRoomID: "!space1:example.com",
			wantForce:  true,
		},
		{
			name:       "valid space room ID with force keyword",
			args:       []string{"force", "!space1:example.com"},
			wantRoomID: "!space1:example.com",
			wantForce:  true,
		},
		{
			name:    "empty args",
			args:    []string{},
			wantErr: true,
		},
		{
			name:    "missing room id",
			args:    []string{"--force"},
			wantErr: true,
		},
		{
			name:    "unexpected extra argument",
			args:    []string{"!space1:example.com", "unknown"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			roomID, force, err := parseDeleteSpaceArgs(tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseDeleteSpaceArgs(%v) error = %v, wantErr = %v", tt.args, err, tt.wantErr)
			}
			if !tt.wantErr {
				if roomID != tt.wantRoomID {
					t.Errorf("roomID = %s, want %s", roomID, tt.wantRoomID)
				}
				if force != tt.wantForce {
					t.Errorf("force = %v, want %v", force, tt.wantForce)
				}
			}
		})
	}
}

func TestStripContactSuffixAny(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{input: "Alice (jmp.chat)", want: "Alice"},
		{input: "Bob (jmpchat)", want: "Bob"},
		{input: "Charlie (jmp)", want: "Charlie"},
		{input: "Dave (cheogram)", want: "Dave"},
		{input: "Eve (sms)", want: "Eve"},
		{input: "+15551234567 (jmp.chat)", want: "+15551234567"},
		{input: "Frank (custom suffix)", want: "Frank"},
		{input: "Grace [SMS]", want: "Grace"},
		{input: "Heidi", want: "Heidi"},
		{input: "   Ivan (JMP.CHAT)   ", want: "Ivan"},
	}

	for _, tt := range tests {
		if got := stripContactSuffixAny(tt.input); got != tt.want {
			t.Errorf("stripContactSuffixAny(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestExtractPhoneDigits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{input: "+15551234567@cheogram.com", want: "5551234567"},
		{input: "+1 (555) 123-4567", want: "5551234567"},
		{input: "555-123-4567", want: "5551234567"},
		{input: "+447911123456@cheogram.com", want: "447911123456"},
		{input: "alice@example.com", want: ""},
		{input: "12345", want: ""}, // too short (< 7 digits)
	}

	for _, tt := range tests {
		if got := extractPhoneDigits(tt.input); got != tt.want {
			t.Errorf("extractPhoneDigits(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestClusterCandidateRooms(t *testing.T) {
	t.Parallel()

	now := time.Now()
	r1 := &candidateRoom{
		roomID:       "!room1:example.com",
		name:         "Alice Smith (jmp.chat)",
		targetJID:    "+15551230001@cheogram.com",
		messageCount: 100,
		lastTS:       now.Add(-1 * time.Hour),
		matchKeys:    map[string]bool{"jid:+15551230001@cheogram.com": true, "phone:5551230001": true, "name:alice smith": true},
	}
	r2 := &candidateRoom{
		roomID:       "!room2:example.com",
		name:         "+1 (555) 123-0001",
		targetJID:    "+15551230001@cheogram.com",
		messageCount: 0,
		lastTS:       now.Add(-10 * time.Hour),
		matchKeys:    map[string]bool{"jid:+15551230001@cheogram.com": true, "phone:5551230001": true},
	}
	r3 := &candidateRoom{
		roomID:       "!room3:example.com",
		name:         "Alice Smith",
		targetJID:    "",
		messageCount: 5,
		lastTS:       now.Add(-2 * time.Hour),
		matchKeys:    map[string]bool{"name:alice smith": true},
	}
	r4 := &candidateRoom{
		roomID:       "!room4:example.com",
		name:         "Bob Jones (jmp.chat)",
		targetJID:    "+15559870002@cheogram.com",
		messageCount: 42,
		lastTS:       now,
		matchKeys:    map[string]bool{"jid:+15559870002@cheogram.com": true, "phone:5559870002": true, "name:bob jones": true},
	}

	clusters := clusterCandidateRooms([]*candidateRoom{r1, r2, r3, r4})

	if len(clusters) != 2 {
		t.Fatalf("len(clusters) = %d, want 2", len(clusters))
	}

	var aliceCluster, bobCluster *duplicateRoomCluster
	for _, c := range clusters {
		if strings.Contains(c.name, "Alice") {
			aliceCluster = c
		} else if strings.Contains(c.name, "Bob") {
			bobCluster = c
		}
	}

	if aliceCluster == nil {
		t.Fatal("missing Alice cluster")
	}
	if aliceCluster.primary.roomID != "!room1:example.com" {
		t.Errorf("Alice primary room = %s, want !room1:example.com", aliceCluster.primary.roomID)
	}
	if len(aliceCluster.duplicates) != 2 {
		t.Errorf("Alice duplicates count = %d, want 2", len(aliceCluster.duplicates))
	}

	if bobCluster == nil {
		t.Fatal("missing Bob cluster")
	}
	if bobCluster.primary.roomID != "!room4:example.com" {
		t.Errorf("Bob primary room = %s, want !room4:example.com", bobCluster.primary.roomID)
	}
	if len(bobCluster.duplicates) != 0 {
		t.Errorf("Bob duplicates count = %d, want 0", len(bobCluster.duplicates))
	}
}
