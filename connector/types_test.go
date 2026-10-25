// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"encoding/json"
	"testing"
)

func TestGhostMetadataJSONRoundTripSpecialCharacters(t *testing.T) {
	t.Parallel()

	original := &GhostMetadata{
		RemoteUserID: "+15550101100@cheogram.com",
		RemoteName:   "O'Brien & \"Sons\" <jmp>\nSecond line",
		AvatarURL:    "mxc://example.com/O'Brien",
		CustomName:   "José 😀 O’Brien",
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal() failed: %v", err)
	}
	var decoded GhostMetadata
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() failed: %v", err)
	}
	if decoded != *original {
		t.Errorf("round trip mismatch:\n got %#v\nwant %#v", decoded, *original)
	}
}

func TestMessageMetadataJSONRoundTripSpecialCharacters(t *testing.T) {
	t.Parallel()

	original := &MessageMetadata{
		Body:          "O'Brien's \"quoted\" 😀 <&> newline\n",
		HasAttachment: true,
		XMPPMessageID: "id'quote",
		OriginID:      "origin-x",
		StanzaID:      "stanza-y",
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal() failed: %v", err)
	}
	var decoded MessageMetadata
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() failed: %v", err)
	}
	if decoded != *original {
		t.Errorf("round trip mismatch:\n got %#v\nwant %#v", decoded, *original)
	}
}
