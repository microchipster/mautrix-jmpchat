package connector

import (
	"reflect"
	"testing"
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
