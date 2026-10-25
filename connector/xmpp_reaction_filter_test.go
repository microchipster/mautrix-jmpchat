// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"reflect"
	"testing"
)

func TestXMPPReactionSupported(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		emoji string
		want  bool
	}{
		{name: "plain emoji", emoji: "👍", want: true},
		{name: "zwj emoji", emoji: "👨‍👩‍👧", want: true},
		{name: "shortcode text", emoji: ":party_parrot:", want: true},
		{name: "padded emoji", emoji: "  ❤️  ", want: true},
		{name: "custom emoji image", emoji: "mxc://example.com/abc123", want: false},
		{name: "custom emoji image uppercase", emoji: "MXC://example.com/abc123", want: false},
		{name: "empty", emoji: "", want: false},
		{name: "whitespace only", emoji: "   ", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := xmppReactionSupported(tc.emoji); got != tc.want {
				t.Errorf("xmppReactionSupported(%q) = %v, want %v", tc.emoji, got, tc.want)
			}
		})
	}
}

func TestFilterXMPPReactions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		reactions []string
		want      []string
	}{
		{
			name:      "keeps plain emoji",
			reactions: []string{"👍", "❤️"},
			want:      []string{"👍", "❤️"},
		},
		{
			name:      "drops image emoji",
			reactions: []string{"👍", "mxc://example.com/abc", "❤️"},
			want:      []string{"👍", "❤️"},
		},
		{
			name:      "all image emoji",
			reactions: []string{"mxc://example.com/abc"},
			want:      []string{},
		},
		{
			name:      "empty input",
			reactions: nil,
			want:      []string{},
		},
		{
			name:      "trims padding",
			reactions: []string{"  👍  "},
			want:      []string{"👍"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := filterXMPPReactions(tc.reactions)
			if got == nil {
				got = []string{}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("filterXMPPReactions(%v) = %v, want %v", tc.reactions, got, tc.want)
			}
		})
	}
}
