// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"strings"
	"testing"
)

func TestParseAvatarMetadata(t *testing.T) {
	t.Parallel()
	raw := []byte(`<metadata xmlns="urn:xmpp:avatar:metadata">` +
		`<info id="609ac9e3db9d32b1c1d1e2f3" type="image/png" bytes="12345" width="64" height="64"/>` +
		`<info id="older" type="image/jpeg" bytes="10"/>` +
		`</metadata>`)
	meta, ok := parseAvatarMetadata(raw)
	if !ok {
		t.Fatal("parseAvatarMetadata() ok = false, want true")
	}
	if len(meta.Info) != 2 {
		t.Fatalf("parseAvatarMetadata() info count = %d, want 2", len(meta.Info))
	}
	if meta.Info[0].ID != "609ac9e3db9d32b1c1d1e2f3" || meta.Info[0].Bytes != 12345 {
		t.Errorf("parseAvatarMetadata() first info = %+v", meta.Info[0])
	}
	description := describeAvatarMetadata(meta)
	for _, want := range []string{"id=609ac9e3db9d32b1c1d1e2f3", "12345 bytes", "64x64", "image/png"} {
		if !strings.Contains(description, want) {
			t.Errorf("describeAvatarMetadata() = %q, missing %q", description, want)
		}
	}
}

func TestParseAvatarMetadataInvalid(t *testing.T) {
	t.Parallel()
	for name, raw := range map[string][]byte{
		"invalid xml": []byte(`<metadata`),
		"no info":     []byte(`<metadata xmlns="urn:xmpp:avatar:metadata"></metadata>`),
		"empty":       {},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, ok := parseAvatarMetadata(raw); ok {
				t.Errorf("parseAvatarMetadata(%q) ok = true, want false", string(raw))
			}
		})
	}
}

func TestDescribeAvatarMetadataEmpty(t *testing.T) {
	t.Parallel()
	if got := describeAvatarMetadata(avatarMetadata{}); got != "" {
		t.Errorf("describeAvatarMetadata() = %q, want empty", got)
	}
	meta, ok := parseAvatarMetadata([]byte(`<metadata xmlns="urn:xmpp:avatar:metadata"><info/></metadata>`))
	if !ok {
		t.Fatal("parseAvatarMetadata() ok = false, want true")
	}
	if got := describeAvatarMetadata(meta); got != "metadata present" {
		t.Errorf("describeAvatarMetadata() = %q, want %q", got, "metadata present")
	}
}

func TestProbeVCardTempNotConnected(t *testing.T) {
	t.Parallel()
	nc := &MyNetworkClient{}
	ctx := t.Context()
	got := nc.probeVCardTemp(ctx, "alice@example.com")
	if !strings.HasPrefix(got, "vcard-temp: error") {
		t.Errorf("probeVCardTemp() = %q, want error prefix", got)
	}
}

func TestParseAvatarData(t *testing.T) {
	t.Parallel()

	size, ok := parseAvatarData([]byte(`<data xmlns="urn:xmpp:avatar:data">aGVsbG8=</data>`))
	if !ok || size != 5 {
		t.Fatalf("parseAvatarData(valid) = (%d, %v), want (5, true)", size, ok)
	}

	size, ok = parseAvatarData([]byte("<data xmlns=\"urn:xmpp:avatar:data\"> aGVs\n bG8= </data>"))
	if !ok || size != 5 {
		t.Fatalf("parseAvatarData(whitespace) = (%d, %v), want (5, true)", size, ok)
	}

	if _, ok = parseAvatarData([]byte("<data xmlns=\"urn:xmpp:avatar:data\"></data>")); ok {
		t.Fatal("parseAvatarData(empty) should not succeed")
	}
	if _, ok = parseAvatarData([]byte("<data xmlns=\"urn:xmpp:avatar:data\">not base64!!</data>")); ok {
		t.Fatal("parseAvatarData(invalid base64) should not succeed")
	}
	if _, ok = parseAvatarData([]byte(`<metadata xmlns="urn:xmpp:avatar:metadata"/>`)); ok {
		t.Fatal("parseAvatarData(non-data payload) should not succeed")
	}
}
