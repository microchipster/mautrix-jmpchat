// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sframe

import (
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

const (
	// Salt is the HKDF salt used by LiveKit and MSC4195.
	Salt = "LKFrameEncryptionKey"

	// SecretSize is the secret length the reference implementation emits.
	// MSC4195 asks for at least 32 bytes, but Element Call generates 16
	// (RTCEncryptionManager.ts:517-521), and so do we for parity.
	SecretSize = 16

	// MinSecretSize is the shortest input secret accepted by DeriveKey.
	// MSC4195 requires at least 32 bytes, but the reference implementation
	// sends 16, and older deployments may shorten it further. Anything
	// shorter than this is treated as a configuration error.
	MinSecretSize = 16

	// AESKeySize is the derived key length (AES-128).
	AESKeySize = 16

	// HKDFInfoSize is the number of zero bytes LiveKit passes as HKDF info.
	HKDFInfoSize = 128
)

var (
	// ErrSecretTooShort is returned when the HKDF input material is shorter
	// than MinSecretSize.
	ErrSecretTooShort = errors.New("sframe: secret is too short")

	// ErrInvalidSecret is returned when a base64 secret cannot be decoded.
	ErrInvalidSecret = errors.New("sframe: invalid secret encoding")
)

// DeriveKey derives the AES-128 frame key from MatrixRTC key material:
//
//	HKDF-SHA256(ikm=secret, salt="LKFrameEncryptionKey", info=0x00*128, L=16)
//
// The input is the raw `media_key.key` bytes from the to-device event, not an
// AES key. It returns a fresh 16-byte slice.
func DeriveKey(secret []byte) ([]byte, error) {
	if len(secret) < MinSecretSize {
		return nil, fmt.Errorf("%w: got %d bytes, need at least %d", ErrSecretTooShort, len(secret), MinSecretSize)
	}

	info := strings.Repeat("\x00", HKDFInfoSize)
	key, err := hkdf.Key(sha256.New, secret, []byte(Salt), info, AESKeySize)
	if err != nil {
		return nil, fmt.Errorf("sframe: derive key: %w", err)
	}
	return key, nil
}

// NewSecret returns SecretSize random bytes suitable for use as HKDF input
// material for a new outbound key.
func NewSecret() ([]byte, error) {
	secret := make([]byte, SecretSize)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("sframe: generate secret: %w", err)
	}
	return secret, nil
}

// DecodeSecretBase64 decodes MatrixRTC key material from base64.
//
// The MSC specifies unpadded base64, while the reference implementation emits
// padded base64 and decodes loosely. Accept standard and URL-safe alphabets,
// padded or unpadded; callers should emit unpadded standard base64.
func DecodeSecretBase64(encoded string) ([]byte, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return nil, fmt.Errorf("%w: empty string", ErrInvalidSecret)
	}

	encodings := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}
	for _, enc := range encodings {
		if secret, err := enc.DecodeString(encoded); err == nil {
			return secret, nil
		}
	}
	return nil, fmt.Errorf("%w: %q", ErrInvalidSecret, encoded)
}
