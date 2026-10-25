// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"testing"
)

func TestResolveGhostDisplayName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		meta      *GhostMetadata
		ghostName string
		jid       string
		want      string
	}{
		{
			name: "custom wins over everything",
			meta: &GhostMetadata{CustomName: "Explicit", CardDAVName: "Card", RemoteName: "Roster"},
			jid:  "+15550101100@cheogram.com",
			want: "Explicit",
		},
		{
			name: "carddav wins over roster",
			meta: &GhostMetadata{CardDAVName: "Card", RemoteName: "Roster"},
			jid:  "+15550101100@cheogram.com",
			want: "Card",
		},
		{
			name: "roster used when no higher source",
			meta: &GhostMetadata{RemoteName: "Roster"},
			jid:  "+15550101100@cheogram.com",
			want: "Roster",
		},
		{
			name:      "ghost name used when metadata empty",
			meta:      &GhostMetadata{},
			ghostName: "Ghost Name",
			jid:       "+15550101100@cheogram.com",
			want:      "Ghost Name",
		},
		{
			name: "label fallback",
			meta: &GhostMetadata{},
			jid:  "+15550101100@cheogram.com",
			want: "+15550101100",
		},
		{
			name:      "nil meta with ghost name",
			meta:      nil,
			ghostName: "Ghost Name",
			jid:       "+15550101100@cheogram.com",
			want:      "Ghost Name",
		},
		{
			name: "whitespace-only sources fall through",
			meta: &GhostMetadata{CustomName: "  ", CardDAVName: " Card ", RemoteName: " "},
			jid:  "+15550101100@cheogram.com",
			want: "Card",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := resolveGhostDisplayName(tc.meta, tc.ghostName, tc.jid); got != tc.want {
				t.Errorf("resolveGhostDisplayName() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseContactNameSource(t *testing.T) {
	t.Parallel()
	tests := []struct {
		raw  string
		want string
		ok   bool
	}{
		{raw: "", want: "", ok: true},
		{raw: "explicit", want: "custom", ok: true},
		{raw: "Custom", want: "custom", ok: true},
		{raw: "carddav", want: "carddav", ok: true},
		{raw: "CardDAV", want: "carddav", ok: true},
		{raw: "roster", want: "roster", ok: true},
		{raw: "xmpp", want: "roster", ok: true},
		{raw: "bogus", ok: false},
		{raw: " carddav ", want: "carddav", ok: true},
	}

	for _, tc := range tests {
		got, ok := parseContactNameSource(tc.raw)
		if got != tc.want || ok != tc.ok {
			t.Errorf("parseContactNameSource(%q) = (%q, %v), want (%q, %v)", tc.raw, got, ok, tc.want, tc.ok)
		}
	}
}

func TestClearContactNameSourcesNilInputs(t *testing.T) {
	t.Parallel()
	count, err := clearContactNameSources(t.Context(), nil, nil, "", "", "jmp")
	if err != nil {
		t.Errorf("clearContactNameSources() error = %v, want nil", err)
	}
	if count != 0 {
		t.Errorf("clearContactNameSources() count = %d, want 0", count)
	}
}

func TestCollectContactJIDsNilInputs(t *testing.T) {
	t.Parallel()
	if jids := collectContactJIDs(t.Context(), nil, nil); jids != nil {
		t.Errorf("collectContactJIDs() = %v, want nil", jids)
	}
}

func TestCountContactNameSourcesNilInputs(t *testing.T) {
	t.Parallel()
	custom, carddav, roster := countContactNameSources(t.Context(), nil, nil)
	if custom != 0 || carddav != 0 || roster != 0 {
		t.Errorf("countContactNameSources() = (%d, %d, %d), want zeros", custom, carddav, roster)
	}
}

func TestSetGhostCardDAVNameNilBridge(t *testing.T) {
	t.Parallel()
	nc := &MyNetworkClient{}
	nc.setGhostCardDAVName(t.Context(), "+15550101100@cheogram.com", "O'Brien")
}
