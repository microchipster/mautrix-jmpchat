// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"strings"
	"testing"
	"time"
)

func TestFormatHistoryDuration(t *testing.T) {
	t.Parallel()
	cases := map[int]string{
		0:    "",
		-3:   "",
		5:    "5s",
		59:   "59s",
		60:   "1m00s",
		65:   "1m05s",
		3600: "60m00s",
	}
	for seconds, want := range cases {
		if got := formatHistoryDuration(seconds); got != want {
			t.Errorf("formatHistoryDuration(%d) = %q, want %q", seconds, got, want)
		}
	}
}

func TestFormatCallHistoryLine(t *testing.T) {
	t.Parallel()
	line := formatCallHistoryLine(CallHistoryEntry{
		Number:          "+15551234567",
		Direction:       "incoming",
		Status:          "completed",
		StartedAt:       time.Now(),
		DurationSeconds: 65,
	})
	for _, want := range []string{"+15551234567", "incoming", "completed", "1m05s"} {
		if !strings.Contains(line, want) {
			t.Errorf("formatCallHistoryLine() = %q, missing %q", line, want)
		}
	}
	bare := formatCallHistoryLine(CallHistoryEntry{
		Number:    "+15551234567",
		Direction: "outgoing",
		StartedAt: time.Now(),
	})
	if !strings.Contains(bare, "+15551234567") || !strings.Contains(bare, "outgoing") {
		t.Errorf("formatCallHistoryLine() = %q, missing number or direction", bare)
	}
	if strings.Contains(bare, "duration") {
		t.Errorf("formatCallHistoryLine() = %q, should not mention duration for a zero-length call", bare)
	}
}

func TestRecordCallHistoryNilLoginSafety(t *testing.T) {
	t.Parallel()
	recordCallHistory(t.Context(), nil, CallHistoryEntry{Number: "+15551234567", Direction: "incoming"})
}
