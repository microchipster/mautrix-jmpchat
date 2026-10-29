// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package callcrypto

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"testing"

	"maunium.net/go/mautrix/id"

	"jmpchat/pkg/sframe"
)

// keyEventPayload builds one io.element.call.encryption_keys payload.
func keyEventPayload(t *testing.T, overrides map[string]any) []byte {
	t.Helper()
	secret, err := sframe.NewSecret()
	if err != nil {
		t.Fatalf("new secret: %v", err)
	}
	entry := map[string]any{"index": 0, "key": base64.RawStdEncoding.EncodeToString(secret)}
	payload := map[string]any{
		"room_id":   "!room:example.com",
		"member_id": "@alice:example.com:DEVICE1",
		"media_key": entry,
		"keys":      entry,
		"member":    map[string]any{"claimed_device_id": "DEVICE1", "id": "@alice:example.com:DEVICE1"},
	}
	for key, value := range overrides {
		payload[key] = value
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return raw
}

func TestParseKeyEvent(t *testing.T) {
	raw := keyEventPayload(t, nil)
	evt, secret, err := parseKeyEvent(raw, "DEVICE1")
	if err != nil {
		t.Fatalf("parseKeyEvent: %v", err)
	}
	if evt.RoomID != "!room:example.com" {
		t.Fatalf("room_id = %q", evt.RoomID)
	}
	if evt.entry().Index != 0 {
		t.Fatalf("index = %d", evt.entry().Index)
	}
	want, err := sframe.DecodeSecretBase64(evt.entry().Key)
	if err != nil {
		t.Fatalf("decode want secret: %v", err)
	}
	if !bytes.Equal(secret, want) {
		t.Fatal("returned secret does not match the payload key")
	}
}

func TestParseKeyEventKeysFallback(t *testing.T) {
	secret, err := sframe.NewSecret()
	if err != nil {
		t.Fatalf("new secret: %v", err)
	}
	raw := keyEventPayload(t, map[string]any{
		"media_key": nil,
		"keys":      map[string]any{"index": 3, "key": base64.RawStdEncoding.EncodeToString(secret)},
	})
	evt, got, err := parseKeyEvent(raw, "DEVICE1")
	if err != nil {
		t.Fatalf("parseKeyEvent: %v", err)
	}
	if evt.entry().Index != 3 {
		t.Fatalf("index = %d, want 3", evt.entry().Index)
	}
	if !bytes.Equal(got, secret) {
		t.Fatal("keys entry was not used")
	}
}

func TestParseKeyEventRejections(t *testing.T) {
	secret, err := sframe.NewSecret()
	if err != nil {
		t.Fatalf("new secret: %v", err)
	}
	encoded := base64.RawStdEncoding.EncodeToString(secret)
	cases := []struct {
		name    string
		payload []byte
		sender  string
	}{
		{"not json", []byte("nope"), "DEVICE1"},
		{"missing room id", keyEventPayload(t, map[string]any{"room_id": ""}), "DEVICE1"},
		{"claimed device mismatch", keyEventPayload(t, nil), "OTHERDEVICE"},
		{"empty sender device", keyEventPayload(t, nil), ""},
		{"missing member", keyEventPayload(t, map[string]any{"member": nil}), "DEVICE1"},
		{"missing member device", keyEventPayload(t, map[string]any{
			"member": map[string]any{"claimed_device_id": "", "id": "@alice:example.com:DEVICE1"},
		}), "DEVICE1"},
		{"missing media key", keyEventPayload(t, map[string]any{"media_key": nil, "keys": nil}), "DEVICE1"},
		{"invalid base64", keyEventPayload(t, map[string]any{
			"media_key": map[string]any{"index": 0, "key": "not base64!"},
			"keys":      nil,
		}), "DEVICE1"},
		{"index out of range", keyEventPayload(t, map[string]any{
			"media_key": map[string]any{"index": uint32(sframe.MaxKID) + 1, "key": encoded},
			"keys":      nil,
		}), "DEVICE1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := parseKeyEvent(tc.payload, id.DeviceID(tc.sender)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}
