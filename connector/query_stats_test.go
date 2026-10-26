// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"strings"
	"testing"
	"time"

	"maunium.net/go/mautrix/id"
)

func TestFormatInteractionDate(t *testing.T) {
	t.Parallel()
	if got := formatInteractionDate(time.Time{}); got != "never" {
		t.Fatalf("zero = %q, want never", got)
	}
	if got := formatInteractionDate(time.Now().Add(-time.Hour)); !strings.HasPrefix(got, "today") {
		t.Fatalf("recent = %q, want today prefix", got)
	}
	if got := formatInteractionDate(time.Now().Add(-30 * time.Hour)); !strings.HasPrefix(got, "yesterday") {
		t.Fatalf("30h = %q, want yesterday prefix", got)
	}
	if got := formatInteractionDate(time.Now().Add(-5*24*time.Hour - time.Hour)); !strings.HasPrefix(got, "5 days ago") {
		t.Fatalf("5d = %q, want 5 days ago prefix", got)
	}
}

func TestFormatFirstInteractionDate(t *testing.T) {
	t.Parallel()
	if got := formatFirstInteractionDate(time.Time{}); got != "never" {
		t.Fatalf("zero = %q, want never", got)
	}
	ts := time.Date(2024, 11, 3, 12, 30, 0, 0, time.UTC)
	if got := formatFirstInteractionDate(ts); got != "2024-11-03" {
		t.Fatalf("date = %q, want 2024-11-03", got)
	}
}

func TestFormatInteractionSummary(t *testing.T) {
	t.Parallel()
	empty := &contactInteraction{}
	if got := formatInteractionSummary(empty); got != "no bridged messages yet" {
		t.Fatalf("empty summary = %q", got)
	}
	ci := &contactInteraction{
		Count:   12,
		FirstTS: time.Date(2024, 11, 3, 12, 30, 0, 0, time.UTC),
		LastTS:  time.Now().Add(-time.Hour),
	}
	got := formatInteractionSummary(ci)
	if !strings.Contains(got, "12 msgs") || !strings.Contains(got, "first 2024-11-03") || !strings.Contains(got, "last today") {
		t.Fatalf("summary = %q", got)
	}
}

func TestFormatContactInteractionLine(t *testing.T) {
	t.Parallel()
	self := &contactInteraction{
		JID:  "me@example.com",
		Self: true,
	}
	line := formatContactInteractionLine(self)
	if !strings.Contains(line, "**You**") || !strings.Contains(line, "me@example.com") || !strings.Contains(line, "your own JMP account") {
		t.Fatalf("self line = %q", line)
	}
	named := &contactInteraction{
		JID:     "+15551234567@cheogram.com",
		Name:    "Alice",
		Count:   3,
		FirstTS: time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC),
		LastTS:  time.Now(),
		RoomID:  id.RoomID("!room:example.com"),
	}
	line = formatContactInteractionLine(named)
	if !strings.Contains(line, "**Alice**") || !strings.Contains(line, "3 msgs") || !strings.Contains(line, "matrix.to") {
		t.Fatalf("named line = %q", line)
	}
	unnamed := &contactInteraction{JID: "bob@example.com"}
	line = formatContactInteractionLine(unnamed)
	if !strings.HasPrefix(line, "- `bob@example.com`") {
		t.Fatalf("unnamed line = %q", line)
	}
}

func TestContactInteractionKey(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"=2b15550101100@cheogram.com": "+15550101100@cheogram.com",
		"Alice@Example.COM":           "alice@example.com",
		"+15551234567@cheogram.com/":  "+15551234567@cheogram.com",
		"  +15551234567@cheogram.com": "+15551234567@cheogram.com",
		"":                            "",
	}
	for in, want := range cases {
		if got := contactInteractionKey(in); got != want {
			t.Errorf("contactInteractionKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGetPortalInteractionStatsNilSafety(t *testing.T) {
	t.Parallel()
	if got := getPortalInteractionStats(t.Context(), nil, nil); got.MessageCount != 0 || !got.FirstTS.IsZero() || !got.LastTS.IsZero() {
		t.Fatalf("nil safety = %+v", got)
	}
}

func TestCollectContactInteractionsNilSafety(t *testing.T) {
	t.Parallel()
	if got := collectContactInteractions(t.Context(), nil, nil); got != nil {
		t.Fatalf("nil bridge = %v, want nil", got)
	}
}
