// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"encoding/xml"
	"testing"

	"gosrc.io/xmpp/stanza"
)

func TestRosterItemNameDecoding(t *testing.T) {
	t.Parallel()

	raw := `<iq type="result"><query xmlns="jabber:iq:roster">` +
		`<item jid="alice@example.com" name="O&apos;Brien &amp; Sons" subscription="both"/>` +
		`<item jid="bob@example.com" name="José 😀" subscription="both"/>` +
		`<item jid="carol@example.com" subscription="remove"/>` +
		`</query></iq>`

	type rosterIQ struct {
		XMLName xml.Name           `xml:"iq"`
		Query   stanza.RosterItems `xml:"jabber:iq:roster query"`
	}
	var iq rosterIQ
	if err := xml.Unmarshal([]byte(raw), &iq); err != nil {
		t.Fatalf("failed to unmarshal roster IQ: %v", err)
	}
	items := iq.Query.Items
	if len(items) != 3 {
		t.Fatalf("decoded %d roster items, want 3", len(items))
	}
	if items[0].Name != "O'Brien & Sons" {
		t.Errorf("first roster item name = %q, want %q", items[0].Name, "O'Brien & Sons")
	}
	if items[1].Name != "José 😀" {
		t.Errorf("second roster item name = %q, want %q", items[1].Name, "José 😀")
	}
	if items[2].Name != "" {
		t.Errorf("remove item name = %q, want empty", items[2].Name)
	}

	if got := normalizeContactName(items[0].Name); got != "O'Brien & Sons" {
		t.Errorf("normalizeContactName(%q) = %q, want %q", items[0].Name, got, "O'Brien & Sons")
	}
}
