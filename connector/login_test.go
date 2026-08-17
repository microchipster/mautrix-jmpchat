// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import "testing"

func TestNormalizeLoginJID(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "trim bare JID", input: " user@cheogram.com ", want: "user@cheogram.com"},
		{name: "reject full JID", input: "user@cheogram.com/resource", wantErr: true},
		{name: "reject server JID", input: "cheogram.com", wantErr: true},
		{name: "reject empty", input: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeLoginJID(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, want error: %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("JID = %q, want %q", got, tt.want)
			}
		})
	}
}
