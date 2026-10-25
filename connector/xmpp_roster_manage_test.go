// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"encoding/xml"
	"strings"
	"testing"

	"gosrc.io/xmpp/stanza"
)

func TestRosterEntryMatches(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		query string
		jid   string
		label string
		want  bool
	}{
		{name: "empty query matches", query: "", jid: "alice@example.com", label: "Alice", want: true},
		{name: "matches label case-insensitively", query: "alice", jid: "bob@example.com", label: "Alice Smith", want: true},
		{name: "matches label substring", query: "brien", jid: "o@example.com", label: "O'Brien", want: true},
		{name: "matches jid substring", query: "example", jid: "alice@example.com", label: "Alice", want: true},
		{name: "no match", query: "zzz", jid: "alice@example.com", label: "Alice", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := rosterEntryMatches(tc.query, tc.jid, tc.label); got != tc.want {
				t.Errorf("rosterEntryMatches(%q, %q, %q) = %v, want %v", tc.query, tc.jid, tc.label, got, tc.want)
			}
		})
	}
}

func TestRosterItemsMarshal(t *testing.T) {
	t.Parallel()
	items := rosterItemsQuery()
	items.AddItem("alice@example.com", "", "", "O'Brien", nil)
	out, err := xml.Marshal(items)
	if err != nil {
		t.Fatalf("xml.Marshal() error: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, `xmlns="jabber:iq:roster"`) {
		t.Errorf("marshalled roster query missing namespace: %s", got)
	}
	if !strings.Contains(got, `jid="alice@example.com"`) {
		t.Errorf("marshalled roster query missing jid: %s", got)
	}
	if !strings.Contains(got, `name="O&#39;Brien"`) {
		t.Errorf("marshalled roster query missing name: %s", got)
	}
}

func TestSetRosterItemNotConnected(t *testing.T) {
	t.Parallel()
	nc := &MyNetworkClient{}
	if err := nc.setRosterItem(context.Background(), "alice@example.com", "Alice"); err == nil {
		t.Error("setRosterItem() on a disconnected client should fail")
	}
	if err := nc.removeRosterItem(context.Background(), "alice@example.com"); err == nil {
		t.Error("removeRosterItem() on a disconnected client should fail")
	}
	if err := nc.setRosterItem(context.Background(), "", "Alice"); err == nil {
		t.Error("setRosterItem() with an empty JID should fail")
	}
}

func TestMarkRosterJID(t *testing.T) {
	t.Parallel()
	nc := &MyNetworkClient{}
	nc.markRosterJID("alice@example.com", true)
	if !nc.rosterItems["alice@example.com"] {
		t.Fatalf("markRosterJID() did not add the JID: %v", nc.rosterItems)
	}
	nc.markRosterJID("alice@example.com", false)
	if nc.rosterItems["alice@example.com"] {
		t.Errorf("markRosterJID() did not remove the JID: %v", nc.rosterItems)
	}
}

func TestRosterItemsQueryNamespace(t *testing.T) {
	t.Parallel()
	items := rosterItemsQuery()
	if items.Namespace() != "jabber:iq:roster" {
		t.Errorf("roster query namespace = %q, want %q", items.Namespace(), "jabber:iq:roster")
	}
	var _ stanza.IQPayload = items
}
