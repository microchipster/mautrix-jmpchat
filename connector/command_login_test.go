// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

func TestLoginMatchesCommandArg(t *testing.T) {
	t.Parallel()
	byRemoteName := &bridgev2.UserLogin{
		UserLogin: &database.UserLogin{ID: networkid.UserLoginID("example-login"), RemoteName: "alice@example.com"},
	}
	byMetadata := &bridgev2.UserLogin{
		UserLogin: &database.UserLogin{
			ID:       networkid.UserLoginID("jmpchat-login"),
			Metadata: &LoginMetadata{RemoteUserID: "+15550101100@cheogram.com"},
		},
	}

	tests := []struct {
		name  string
		login *bridgev2.UserLogin
		arg   string
		want  bool
	}{
		{name: "remote name exact", login: byRemoteName, arg: "alice@example.com", want: true},
		{name: "remote name case-insensitive local", login: byRemoteName, arg: "Alice@Example.com", want: true},
		{name: "login id match", login: byRemoteName, arg: "example-login", want: true},
		{name: "login id no match", login: byRemoteName, arg: "other-login", want: false},
		{name: "metadata remote jid", login: byMetadata, arg: "+15550101100@cheogram.com", want: true},
		{name: "metadata remote jid escaped", login: byMetadata, arg: "=2b15550101100@cheogram.com", want: true},
		{name: "metadata phone number", login: byMetadata, arg: "+15550101100", want: true},
		{name: "other contact", login: byMetadata, arg: "bob@example.com", want: false},
		{name: "empty arg", login: byMetadata, arg: "", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := loginMatchesCommandArg(tc.login, tc.arg)
			if got != tc.want {
				t.Errorf("loginMatchesCommandArg(%q) = %v, want %v", tc.arg, got, tc.want)
			}
		})
	}
}

func TestSelectCommandLoginNilEvent(t *testing.T) {
	t.Parallel()
	login, args := selectCommandLogin(nil)
	if login != nil || args != nil {
		t.Errorf("selectCommandLogin(nil) = (%v, %v), want (nil, nil)", login, args)
	}
}
