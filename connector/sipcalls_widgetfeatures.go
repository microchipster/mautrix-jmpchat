//go:build sipcalls

// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"maunium.net/go/mautrix/bridgev2"

	"github.com/williamkray/mrtc-sip-bridge/pkg/hostkit"
)

// widgetFeatures implements the optional Dialpad widget contact features on
// top of our roster helpers. The widget passes information in as plain values,
// so every method is independent and safe to call from HTTP handler
// goroutines.
type widgetFeatures struct{ x *callsExt }

var (
	_ hostkit.RosterOnlyPrefs = (*widgetFeatures)(nil)
	_ hostkit.ChatOpener      = (*widgetFeatures)(nil)
	_ hostkit.ContactSaver    = (*widgetFeatures)(nil)
	_ hostkit.ContactLookup   = (*widgetFeatures)(nil)
	_ hostkit.ContactLister   = (*widgetFeatures)(nil)
)

// RosterOnly reports whether incoming calls are limited to roster contacts.
func (f *widgetFeatures) RosterOnly(ul *bridgev2.UserLogin) hostkit.RosterOnlyState {
	meta := loginMeta(ul)
	if meta == nil || !meta.CallRosterOnly {
		return hostkit.RosterOnlyState{}
	}
	return hostkit.RosterOnlyState{On: true, Warning: f.x.rosterOnlyWarning(ul)}
}

// SetRosterOnly stores the roster-only preference for a login.
func (f *widgetFeatures) SetRosterOnly(ctx context.Context, ul *bridgev2.UserLogin, on bool) (hostkit.RosterOnlyState, error) {
	if ul == nil {
		return hostkit.RosterOnlyState{}, fmt.Errorf("no login provided")
	}
	meta := loginMeta(ul)
	if meta == nil {
		return hostkit.RosterOnlyState{}, fmt.Errorf("login metadata is not available")
	}
	x := f.x
	x.saveMu.Lock()
	prev := meta.CallRosterOnly
	meta.CallRosterOnly = on
	saveErr := ul.Save(ctx)
	if saveErr != nil {
		meta.CallRosterOnly = prev
	}
	x.saveMu.Unlock()
	if saveErr != nil {
		return hostkit.RosterOnlyState{}, fmt.Errorf("failed to save roster-only preference: %w", saveErr)
	}
	state := hostkit.RosterOnlyState{On: on}
	if on {
		state.Warning = x.rosterOnlyWarning(ul)
	}
	return state, nil
}

// OpenChat opens the DM portal for a phone number, creating it when needed.
func (f *widgetFeatures) OpenChat(ctx context.Context, ul *bridgev2.UserLogin, e164 string) (hostkit.ChatRoom, error) {
	client, err := f.x.clientFor(ul)
	if err != nil {
		return hostkit.ChatRoom{}, err
	}
	number := strings.TrimSpace(e164)
	if number == "" {
		return hostkit.ChatRoom{}, hostkit.NewActionError(hostkit.CodeInvalidName, "No phone number provided.")
	}
	jid := number + "@cheogram.com"
	portal, err := f.x.br.GetExistingPortalByKey(ctx, client.effectivePortalKey(MakePortalID(jid)))
	if err == nil && portal != nil && portal.MXID != "" {
		return hostkit.ChatRoom{RoomID: portal.MXID}, nil
	}
	if err := client.createDM(ctx, jid); err != nil {
		return hostkit.ChatRoom{}, hostkit.NewActionError(hostkit.CodeUnavailable, "Could not start a chat with this number.")
	}
	return hostkit.ChatRoom{Pending: true}, nil
}

// SaveContact adds the number to the XMPP roster, or renames it.
func (f *widgetFeatures) SaveContact(ctx context.Context, ul *bridgev2.UserLogin, e164, name string) (hostkit.SavedContact, error) {
	client, err := f.x.clientFor(ul)
	if err != nil {
		return hostkit.SavedContact{}, err
	}
	clean, ok := hostkit.CleanContactName(name)
	if !ok {
		return hostkit.SavedContact{}, hostkit.NewActionError(hostkit.CodeInvalidName, "That contact name is not valid.")
	}
	number := strings.TrimSpace(e164)
	if number == "" {
		return hostkit.SavedContact{}, hostkit.NewActionError(hostkit.CodeInvalidName, "No phone number provided.")
	}
	jid := number + "@cheogram.com"
	prev := ""
	if ghost, err := f.x.br.GetGhostByID(ctx, MakeUserID(jid)); err == nil && ghost != nil {
		if meta, ok := ghost.Metadata.(*GhostMetadata); ok && meta != nil {
			prev = strings.TrimSpace(meta.RemoteName)
		}
	}
	if prev == clean {
		return hostkit.SavedContact{Name: clean, PreviousName: prev, Changed: false}, nil
	}
	if err := client.setRosterItemCore(ctx, jid, clean); err != nil {
		return hostkit.SavedContact{}, hostkit.NewActionError(hostkit.CodeUnavailable, "Not connected to the XMPP server.")
	}
	changed := client.updateRosterContactName(jid, clean)
	if changed {
		client.refreshRosterNamedPortals()
	}
	return hostkit.SavedContact{Name: clean, PreviousName: prev, Changed: true}, nil
}

// ContactName reports the saved roster name of a number, when known.
func (f *widgetFeatures) ContactName(ul *bridgev2.UserLogin, e164 string) (string, bool) {
	if ul == nil {
		return "", false
	}
	client, _ := ul.Client.(*MyNetworkClient)
	if client == nil {
		return "", false
	}
	number := strings.TrimSpace(e164)
	if number == "" {
		return "", false
	}
	jid := number + "@cheogram.com"
	client.rosterMu.RLock()
	saved := client.rosterItems[jid]
	client.rosterMu.RUnlock()
	if !saved {
		return "", false
	}
	// Resolve the display name from our local ghost/cache layer; this only
	// reads the bridge database, never the network.
	name := strings.TrimSpace(contactDisplayName(context.Background(), f.x.br, jid))
	if name == "" || name == jid {
		return "", true
	}
	return name, true
}

// ListContacts pages through roster contacts that are phone numbers.
func (f *widgetFeatures) ListContacts(ul *bridgev2.UserLogin, query string, offset, limit int) (hostkit.ContactPage, error) {
	client, err := f.x.clientFor(ul)
	if err != nil {
		return hostkit.ContactPage{}, err
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	var all []hostkit.ContactEntry
	for _, jid := range client.rosterContactList() {
		number, ok := e164FromCheogramJID(jid)
		if !ok {
			continue
		}
		name := hostkit.DisplayContactName(contactDisplayName(context.Background(), f.x.br, jid))
		if name == "" {
			continue
		}
		all = append(all, hostkit.ContactEntry{Name: name, E164: number})
	}
	if len(all) == 0 {
		return hostkit.ContactPage{}, nil
	}
	q := strings.TrimSpace(query)
	filtered := filterWidgetContacts(all, q)
	sort.SliceStable(filtered, func(i, j int) bool {
		li, lj := strings.ToLower(filtered[i].Name), strings.ToLower(filtered[j].Name)
		if li != lj {
			return li < lj
		}
		return filtered[i].E164 < filtered[j].E164
	})
	total := len(filtered)
	page := []hostkit.ContactEntry{}
	if offset < total {
		end := offset + limit
		if end > total {
			end = total
		}
		page = filtered[offset:end]
	}
	return hostkit.ContactPage{Items: page, Total: total, Loaded: true}, nil
}

// filterWidgetContacts applies the widget query rules: a name substring, or
// (for digit-only queries with at least two digits) a digit substring of the
// phone number.
func filterWidgetContacts(contacts []hostkit.ContactEntry, query string) []hostkit.ContactEntry {
	if query == "" {
		return append([]hostkit.ContactEntry(nil), contacts...)
	}
	if digits, ok := widgetQueryDigits(query); ok {
		var out []hostkit.ContactEntry
		for _, c := range contacts {
			if strings.Contains(digitsOnly(c.E164), digits) {
				out = append(out, c)
			}
		}
		return out
	}
	lowered := strings.ToLower(query)
	var out []hostkit.ContactEntry
	for _, c := range contacts {
		if strings.Contains(strings.ToLower(c.Name), lowered) {
			out = append(out, c)
		}
	}
	return out
}

// widgetQueryDigits reports whether the query is a phone-number-style search
// (only digits and common separators, at least two digits) and returns its
// digits.
func widgetQueryDigits(query string) (string, bool) {
	var digits strings.Builder
	for _, r := range query {
		switch {
		case r >= '0' && r <= '9':
			digits.WriteRune(r)
		case r == '+' || r == '-' || r == '(' || r == ')' || r == '.' || r == ' ':
		default:
			return "", false
		}
	}
	out := digits.String()
	return out, len(out) >= 2
}

func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// rosterOnlyWarning explains when roster-only calls cannot take effect.
func (x *callsExt) rosterOnlyWarning(ul *bridgev2.UserLogin) string {
	if ul == nil {
		return ""
	}
	client, _ := ul.Client.(*MyNetworkClient)
	if client == nil {
		return ""
	}
	client.rosterMu.RLock()
	count := len(client.rosterItems)
	client.rosterMu.RUnlock()
	if count == 0 {
		return "Your Jabber roster has not loaded (or is empty), so calls will still ring for everyone."
	}
	return ""
}

// clientFor resolves the XMPP client of a login with a user-friendly error.
func (x *callsExt) clientFor(ul *bridgev2.UserLogin) (*MyNetworkClient, error) {
	if ul == nil {
		return nil, hostkit.NewActionError(hostkit.CodeUnavailable, "No login available.")
	}
	client, _ := ul.Client.(*MyNetworkClient)
	if client == nil {
		return nil, hostkit.NewActionError(hostkit.CodeUnavailable, "This login is not connected.")
	}
	return client, nil
}
