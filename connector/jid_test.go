// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"testing"
)

func TestCanonicalBareJID(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{
			input: "user@example.com/mobile",
			want:  "user@example.com",
		},
		{
			input: "  user@EXAMPLE.COM/resource  ",
			want:  "user@example.com",
		},
		{
			input: "+5550102100@cheogram.com/res",
			want:  "+5550102100@cheogram.com",
		},
		{
			input: "+5550102200,+5550102100@cheogram.com",
			want:  "+5550102100,+5550102200@cheogram.com",
		},
		{
			input: "+5550102100,+5550102200@CHEOGRAM.COM/phone",
			want:  "+5550102100,+5550102200@cheogram.com",
		},
		{
			input: "5550102100@cheogram.com",
			want:  "+5550102100@cheogram.com",
		},
		{
			input: "room@conference.example.com/nick",
			want:  "room@conference.example.com",
		},
	}

	for _, tt := range tests {
		got := CanonicalBareJID(tt.input)
		if got != tt.want {
			t.Errorf("CanonicalBareJID(%q) = %q; want %q", tt.input, got, tt.want)
		}
	}
}

func TestMakePortalIDAndUserIDConsistency(t *testing.T) {
	jid1 := "+5550102200,+5550102100@cheogram.com/res"
	jid2 := "+5550102100,+5550102200@cheogram.com"

	p1 := MakePortalID(jid1)
	p2 := MakePortalID(jid2)
	if p1 != p2 {
		t.Errorf("MakePortalID mismatch for permutations: %q vs %q", p1, p2)
	}

	u1 := MakeUserID("user@EXAMPLE.COM/desktop")
	u2 := MakeUserID("user@example.com/mobile")
	if u1 != u2 {
		t.Errorf("MakeUserID mismatch for resources: %q vs %q", u1, u2)
	}
}
