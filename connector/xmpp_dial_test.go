// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import "testing"

func TestXMPPJIDWithResource(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		jid    string
		prefix string
		want   string
	}{
		"no prefix":         {"user@example.com", "", "user@example.com"},
		"prefix":            {"user@example.com", "bridge", "user@example.com/bridge"},
		"replaces old":      {"user@example.com/old", "bridge", "user@example.com/bridge"},
		"blank prefix":      {"user@example.com", "   ", "user@example.com"},
		"empty jid":         {"", "bridge", ""},
		"trimmed prefix":    {"user@example.com", "  bridge  ", "user@example.com/bridge"},
		"resource cheogram": {"+15550101100@cheogram.com", "jmp", "+15550101100@cheogram.com/jmp"},
	}
	for name, tc := range cases {
		if got := xmppJIDWithResource(tc.jid, tc.prefix); got != tc.want {
			t.Errorf("%s: xmppJIDWithResource(%q, %q) = %q, want %q", name, tc.jid, tc.prefix, got, tc.want)
		}
	}
}

func TestXMPPServerAddress(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		server string
		port   int
		want   string
	}{
		"empty":             {"", 0, ""},
		"host default":      {"xmpp.example.com", 0, "xmpp.example.com:5222"},
		"host with port":    {"xmpp.example.com:5223", 0, "xmpp.example.com:5223"},
		"port override":     {"xmpp.example.com", 5223, "xmpp.example.com:5223"},
		"ipv6 default":      {"::1", 0, "[::1]:5222"},
		"ipv6 with port":    {"[::1]:5223", 0, "[::1]:5223"},
		"ipv6 override":     {"[::1]:5223", 5222, "[::1]:5222"},
		"trimmed":           {"  host.example.com  ", 0, "host.example.com:5222"},
		"port on bare ipv6": {"fe80::1", 5222, "[fe80::1]:5222"},
	}
	for name, tc := range cases {
		if got := xmppServerAddress(tc.server, tc.port); got != tc.want {
			t.Errorf("%s: xmppServerAddress(%q, %d) = %q, want %q", name, tc.server, tc.port, got, tc.want)
		}
	}
}
