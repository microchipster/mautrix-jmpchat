// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sframe

import (
	"bytes"
	"errors"
	"testing"
)

func TestDeriveKeyLengthValidation(t *testing.T) {
	for _, secret := range [][]byte{nil, {}, make([]byte, MinSecretSize-1)} {
		if _, err := DeriveKey(secret); !errors.Is(err, ErrSecretTooShort) {
			t.Fatalf("DeriveKey(%d bytes) error = %v, want ErrSecretTooShort", len(secret), err)
		}
	}

	// Longer-than-reference secrets are accepted (MSC4195 asks for 32).
	long := bytes.Repeat([]byte{0x42}, 32)
	key, err := DeriveKey(long)
	if err != nil {
		t.Fatalf("DeriveKey(32 bytes): %v", err)
	}
	if len(key) != AESKeySize {
		t.Fatalf("key length = %d, want %d", len(key), AESKeySize)
	}
}

func TestDeriveKeyDeterministic(t *testing.T) {
	secret := mustHex(t, "000102030405060708090a0b0c0d0e0f")
	first, err := DeriveKey(secret)
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	second, err := DeriveKey(secret)
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("DeriveKey is not deterministic")
	}

	// The returned slice must be independent of other calls.
	first[0] ^= 0xff
	if bytes.Equal(first, second) {
		t.Fatal("DeriveKey returned shared backing memory")
	}
}

func TestNewSecret(t *testing.T) {
	first, err := NewSecret()
	if err != nil {
		t.Fatalf("NewSecret: %v", err)
	}
	second, err := NewSecret()
	if err != nil {
		t.Fatalf("NewSecret: %v", err)
	}
	if len(first) != SecretSize {
		t.Fatalf("secret length = %d, want %d", len(first), SecretSize)
	}
	if bytes.Equal(first, second) {
		t.Fatal("two generated secrets are identical")
	}
}

func TestDecodeSecretBase64(t *testing.T) {
	// 0xfb 0xff encodes to +/8= in standard base64 and -_8= in URL-safe.
	raw := []byte{0xfb, 0xff}

	tests := []struct {
		name    string
		encoded string
	}{
		{name: "standard padded", encoded: "+/8="},
		{name: "standard unpadded", encoded: "+/8"},
		{name: "url padded", encoded: "-_8="},
		{name: "url unpadded", encoded: "-_8"},
		{name: "surrounding whitespace", encoded: " +/8= \n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decoded, err := DecodeSecretBase64(test.encoded)
			if err != nil {
				t.Fatalf("DecodeSecretBase64(%q): %v", test.encoded, err)
			}
			if !bytes.Equal(decoded, raw) {
				t.Fatalf("decoded = %x, want %x", decoded, raw)
			}
		})
	}

	for _, encoded := range []string{"", "   ", "not base64!", "="} {
		if _, err := DecodeSecretBase64(encoded); !errors.Is(err, ErrInvalidSecret) {
			t.Fatalf("DecodeSecretBase64(%q) error = %v, want ErrInvalidSecret", encoded, err)
		}
	}
}
