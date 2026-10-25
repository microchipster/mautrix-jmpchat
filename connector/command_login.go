// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"strings"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/commands"
)

// selectCommandLogin resolves an optional leading login JID argument.
//
// If the first argument matches one of the user's own logins (by login ID,
// stored remote JID or the resolved network user ID) and removing it still
// leaves at least one argument for the command itself, that login is used.
// Otherwise the default login and the original argument list are returned.
func selectCommandLogin(ce *commands.Event) (*bridgev2.UserLogin, []string) {
	if ce == nil {
		return nil, nil
	}
	if len(ce.Args) >= 2 {
		candidate := strings.TrimSpace(ce.Args[0])
		if candidate != "" && strings.Contains(candidate, "@") {
			for _, login := range ce.User.GetUserLogins() {
				if login == nil {
					continue
				}
				if loginMatchesCommandArg(login, candidate) {
					return login, ce.Args[1:]
				}
			}
		}
	}
	return ce.User.GetDefaultLogin(), ce.Args
}

// loginMatchesCommandArg reports whether arg refers to the given login.
func loginMatchesCommandArg(login *bridgev2.UserLogin, arg string) bool {
	argCanon := CanonicalBareJID(UnescapeJID(arg))
	candidates := []string{string(login.ID), login.RemoteName}
	if meta, ok := login.Metadata.(*LoginMetadata); ok && meta != nil && meta.RemoteUserID != "" {
		candidates = append(candidates, meta.RemoteUserID)
	}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if strings.EqualFold(candidate, arg) {
			return true
		}
		if argCanon != "" && CanonicalBareJID(candidate) == argCanon {
			return true
		}
	}
	return false
}
