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
			want:  "+15550102100@cheogram.com",
		},
		{
			input: "555-123-4567@cheogram.com",
			want:  "+15551234567@cheogram.com",
		},
		{
			input: "5551234@cheogram.com",
			want:  "+15551234@cheogram.com",
		},
		{
			input: "+44 7123 456789@cheogram.com",
			want:  "+447123456789@cheogram.com",
		},
		{
			input: "5551234567,555-123-4567,+447123456789@cheogram.com",
			want:  "+15551234567,+447123456789@cheogram.com",
		},
		{
			input: "room@conference.example.com/nick",
			want:  "room@conference.example.com",
		},
		{
			input: "69061;phone-context=ca-us.phone-context.soprani.ca@cheogram.com",
			want:  "69061@cheogram.com",
		},
		{
			input: "+15551234567;phone-context=ca-us.phone-context.soprani.ca@cheogram.com",
			want:  "+15551234567@cheogram.com",
		},
		{
			input: "69061;phone-context=ca-us.phone-context.soprani.ca",
			want:  "69061@cheogram.com",
		},
	}

	for _, tt := range tests {
		got := CanonicalBareJID(tt.input)
		if got != tt.want {
			t.Errorf("CanonicalBareJID(%q) = %q; want %q", tt.input, got, tt.want)
		}
	}
}

func TestCanonicalBareJIDPreservesNonPhoneCheogramLocalparts(t *testing.T) {
	if got := CanonicalBareJID("Alice@cheogram.com/resource"); got != "alice@cheogram.com" {
		t.Fatalf("CanonicalBareJID() = %q, want %q", got, "alice@cheogram.com")
	}
	if got := CanonicalBareJID("alice,bob@cheogram.com"); got != "alice,bob@cheogram.com" {
		t.Fatalf("CanonicalBareJID() = %q, want %q", got, "alice,bob@cheogram.com")
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

func TestSpecialCharactersInJIDs(t *testing.T) {
	t.Parallel()

	if got := CanonicalBareJID("o'brien@Example.COM/resource"); got != "o'brien@example.com" {
		t.Fatalf("CanonicalBareJID() = %q, want %q", got, "o'brien@example.com")
	}
	if got := CanonicalBareJID("o’brien@example.com"); got != "o’brien@example.com" {
		t.Fatalf("CanonicalBareJID() = %q, want %q", got, "o’brien@example.com")
	}
	if got := contactLabelFromJID("o'brien@example.com"); got != "o'brien" {
		t.Fatalf("contactLabelFromJID() = %q, want %q", got, "o'brien")
	}
	if got := contactLabelFromJID("O'Brien@Example.com/Nick"); got != "Nick" {
		t.Fatalf("contactLabelFromJID() = %q, want %q", got, "Nick")
	}
	if got := MakeUserID("o'brien@example.com"); got != "o'brien@example.com" {
		t.Fatalf("MakeUserID() = %q, want %q", got, "o'brien@example.com")
	}
}

func TestE164FromCheogramJID(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		want   string
		wantOk bool
	}{
		"+15550101100@cheogram.com":                  {"+15550101100", true},
		"5550101100@cheogram.com":                    {"+15550101100", true},
		"+15550101100":                               {"+15550101100", true},
		"=2b15550101100@cheogram.com":                {"+15550101100", true},
		"+15550101100;phone-context=ca@cheogram.com": {"+15550101100", true},
		"69061@cheogram.com":                         {"", false},
		"+15550101100,+15550101200@cheogram.com":     {"", false},
		"alice@example.com":                          {"", false},
		"":                                           {"", false},
	}
	for input, want := range tests {
		got, ok := e164FromCheogramJID(input)
		if got != want.want || ok != want.wantOk {
			t.Errorf("e164FromCheogramJID(%q) = (%q, %v), want (%q, %v)", input, got, ok, want.want, want.wantOk)
		}
	}
}

func TestIsSelfBareJID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		own  string
		jid  string
		want bool
	}{
		{name: "exact", own: "alice@example.com", jid: "alice@example.com", want: true},
		{name: "with resource", own: "alice@example.com", jid: "alice@example.com/phone", want: true},
		{name: "mixed case", own: "alice@example.com", jid: "Alice@Example.Com", want: true},
		{name: "own with resource", own: "alice@example.com/phone", jid: "alice@example.com", want: true},
		{name: "surrounding spaces", own: " alice@example.com ", jid: "alice@example.com", want: true},
		{name: "escaped cheogram JID", own: "+15550101100@cheogram.com", jid: "=2b15550101100@cheogram.com", want: true},
		{name: "different localpart", own: "alice@example.com", jid: "bob@example.com", want: false},
		{name: "different domain", own: "alice@example.com", jid: "alice@example.org", want: false},
		{name: "empty own JID", own: "", jid: "alice@example.com", want: false},
		{name: "empty candidate", own: "alice@example.com", jid: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isSelfBareJID(tt.own, tt.jid); got != tt.want {
				t.Errorf("isSelfBareJID(%q, %q) = %t, want %t", tt.own, tt.jid, got, tt.want)
			}
		})
	}
}

func TestIsBareServerAddress(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"example.com", true},
		{"cheogram.com", true},
		{"user@example.com", false},
		{"+15550102100@cheogram.com", false},
		{"alice", false},
		{"", false},
	}
	for _, test := range tests {
		if got := isBareServerAddress(test.input); got != test.want {
			t.Errorf("isBareServerAddress(%q) = %v, want %v", test.input, got, test.want)
		}
	}
}
