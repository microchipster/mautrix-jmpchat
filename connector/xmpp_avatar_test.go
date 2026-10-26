// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"encoding/xml"
	"errors"
	"fmt"
	"testing"
	"time"

	"gosrc.io/xmpp/stanza"
	"maunium.net/go/mautrix/bridgev2"
)

func TestVCardAvatarNeedsRefresh(t *testing.T) {
	t.Parallel()
	now := time.Now()
	recent := now.Add(-time.Hour)
	stale := now.Add(-25 * time.Hour)
	cases := map[string]struct {
		meta *GhostMetadata
		want bool
	}{
		"nil meta":               {nil, false},
		"no hash":                {&GhostMetadata{}, false},
		"never fetched":          {&GhostMetadata{AvatarHash: "abc"}, true},
		"fetched recently":       {&GhostMetadata{AvatarHash: "abc", AvatarFetchedAt: &recent}, false},
		"fetched long ago":       {&GhostMetadata{AvatarHash: "abc", AvatarFetchedAt: &stale}, true},
		"hash without timestamp": {&GhostMetadata{AvatarHash: "abc", AvatarFetchedAt: nil}, true},
	}
	for name, tc := range cases {
		if got := vcardAvatarNeedsRefresh(tc.meta, now); got != tc.want {
			t.Errorf("%s: vcardAvatarNeedsRefresh = %v, want %v", name, got, tc.want)
		}
	}
}

func TestIsPermanentVCardError(t *testing.T) {
	t.Parallel()
	permanent := permanentVCardError{fmt.Errorf("vCard request failed: error")}
	if !isPermanentVCardError(permanent) {
		t.Error("permanentVCardError should be detected")
	}
	if !isPermanentVCardError(fmt.Errorf("wrapped: %w", permanent)) {
		t.Error("wrapped permanentVCardError should be detected")
	}
	if isPermanentVCardError(errors.New("plain error")) {
		t.Error("plain error should not be treated as permanent")
	}
	if isPermanentVCardError(nil) {
		t.Error("nil should not be treated as permanent")
	}
}

func TestXMPPVCardUpdatePresenceDecode(t *testing.T) {
	t.Parallel()
	var pres stanza.Presence
	err := xml.Unmarshal([]byte(`<presence from="alice@example.com/res" type="available">`+
		`<x xmlns="vcard-temp:x:update"><photo>abc123</photo></x>`+
		`</presence>`), &pres)
	if err != nil {
		t.Fatalf("failed to decode presence: %v", err)
	}
	var update XMPPVCardUpdate
	if !pres.Get(&update) {
		t.Fatalf("expected vcard update extension to be found")
	}
	if update.Photo != "abc123" {
		t.Errorf("photo hash = %q, want %q", update.Photo, "abc123")
	}
}

func TestVCardPhotoDecode(t *testing.T) {
	t.Parallel()
	var iq stanza.IQ
	err := xml.Unmarshal([]byte(`<iq type="result" from="alice@example.com" id="v1">`+
		`<vCard xmlns="vcard-temp">`+
		`<PHOTO><TYPE>image/png</TYPE><BINVAL>aGVsbG8=</BINVAL></PHOTO>`+
		`</vCard>`+
		`</iq>`), &iq)
	if err != nil {
		t.Fatalf("failed to decode IQ: %v", err)
	}
	vcard, ok := iq.Payload.(*vcardTemp)
	if !ok || vcard == nil {
		t.Fatalf("expected *vcardTemp payload, got %T", iq.Payload)
	}
	if vcard.Photo == nil {
		t.Fatalf("expected PHOTO to be decoded")
	}
	if vcard.Photo.Type != "image/png" {
		t.Errorf("photo type = %q, want image/png", vcard.Photo.Type)
	}
	if vcard.Photo.Binval != "aGVsbG8=" {
		t.Errorf("photo binval = %q, want aGVsbG8=", vcard.Photo.Binval)
	}
	if vcard.Namespace() != NSVCardTemp {
		t.Errorf("namespace = %q, want %q", vcard.Namespace(), NSVCardTemp)
	}
}

func TestVCardWithoutPhotoDecode(t *testing.T) {
	t.Parallel()
	var iq stanza.IQ
	err := xml.Unmarshal([]byte(`<iq type="result" from="alice@example.com" id="v2">`+
		`<vCard xmlns="vcard-temp"><FN>Alice</FN></vCard>`+
		`</iq>`), &iq)
	if err != nil {
		t.Fatalf("failed to decode IQ: %v", err)
	}
	vcard, ok := iq.Payload.(*vcardTemp)
	if !ok || vcard == nil {
		t.Fatalf("expected *vcardTemp payload, got %T", iq.Payload)
	}
	if vcard.Photo != nil {
		t.Errorf("expected no PHOTO, got %+v", vcard.Photo)
	}
}

func TestAllowedVCardAvatarMime(t *testing.T) {
	t.Parallel()
	tests := map[string]bool{
		"image/png":                true,
		"image/jpeg":               true,
		"image/gif":                true,
		"image/webp":               true,
		"image/svg+xml":            false,
		"application/octet-stream": false,
		"text/plain":               false,
		"":                         false,
	}
	for mime, want := range tests {
		if got := allowedVCardAvatarMime(mime); got != want {
			t.Errorf("allowedVCardAvatarMime(%q) = %v, want %v", mime, got, want)
		}
	}
}

func TestHandleVCardUpdatePresenceGuards(t *testing.T) {
	t.Parallel()
	nc := &MyNetworkClient{}
	var pres stanza.Presence
	_ = xml.Unmarshal([]byte(`<presence from="+15550101100@cheogram.com/res">`+
		`<x xmlns="vcard-temp:x:update"><photo>abc</photo></x>`+
		`</presence>`), &pres)
	// No bridge configured: must be a no-op, including for cheogram numbers.
	nc.handleVCardUpdatePresence("+15550101100@cheogram.com", pres)
	nc.handleVCardUpdatePresence("", pres)
}

func TestFetchVCardPhotoNotConnected(t *testing.T) {
	t.Parallel()
	nc := &MyNetworkClient{}
	_, _, err := nc.fetchVCardPhoto(t.Context(), "alice@example.com")
	if err == nil {
		t.Fatalf("expected error when not connected")
	}
}

func TestRefreshContactPortalAvatarsNilSafety(t *testing.T) {
	t.Parallel()
	nc := &MyNetworkClient{}
	nc.refreshContactPortalAvatars(t.Context(), "+15551234567@cheogram.com", &bridgev2.Avatar{MXC: "mxc://example.com/abc"})
	nc.refreshContactPortalAvatars(t.Context(), "x", nil)
	refreshContactPortalAvatarsFor(t.Context(), nil, nil, "x", &bridgev2.Avatar{MXC: "mxc://example.com/abc"})
	refreshContactPortalAvatarsFor(t.Context(), nil, nil, "x", &bridgev2.Avatar{Remove: true})
}

func TestForceRefreshAvatarGuards(t *testing.T) {
	t.Parallel()
	nc := &MyNetworkClient{}
	if err := nc.forceRefreshAvatar(t.Context(), "+15551234567@cheogram.com"); err == nil {
		t.Fatal("expected an error without a bridge")
	}
}
