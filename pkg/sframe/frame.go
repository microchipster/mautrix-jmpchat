// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sframe

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
)

const (
	// IVSize is the IV length this package emits and the value of the
	// transmitted IV length byte.
	IVSize = 12

	// FrameOverhead is the number of bytes added to every payload:
	// 16 GCM tag + 12 IV + 1 IV length + 1 KID. The cleartext Opus TOC byte
	// is not overhead: it is the first payload byte moved to the front
	// unencrypted.
	FrameOverhead = tagSize + IVSize + trailerSize

	// opusTOCSize is the number of cleartext, authenticated bytes at the
	// start of the payload (UNENCRYPTED_BYTES.audio in livekit-client).
	opusTOCSize = 1

	// trailerSize is the IV length byte plus the KID byte.
	trailerSize = 2

	// tagSize is the AES-GCM authentication tag length.
	tagSize = 16
)

var (
	// ErrKeySize is returned when an AES key is not AESKeySize bytes.
	ErrKeySize = errors.New("sframe: key must be 16 bytes")

	// ErrIVSize is returned when SealWithIV gets an IV that is not IVSize
	// bytes long.
	ErrIVSize = errors.New("sframe: IV must be 12 bytes")

	// ErrEmptyFrame is returned when asked to seal an empty payload.
	ErrEmptyFrame = errors.New("sframe: empty payload")

	// ErrTruncatedFrame is returned for frames too short to contain a
	// trailer, IV, ciphertext, and Opus TOC byte.
	ErrTruncatedFrame = errors.New("sframe: truncated frame")

	// ErrIVLength is returned when the transmitted IV length byte describes
	// an IV that does not fit in the frame.
	ErrIVLength = errors.New("sframe: invalid IV length")

	// ErrDecrypt is returned when AES-GCM authentication fails: a wrong key,
	// a tampered ciphertext/tag, a tampered IV, or a tampered TOC byte.
	ErrDecrypt = errors.New("sframe: decrypt failed")

	// ErrSIF is returned by KeyTable.Open for plaintext server-injected
	// frames. Such frames carry no key material and must be dropped, not
	// decrypted.
	ErrSIF = errors.New("sframe: server-injected frame")
)

// Seal encrypts a single Opus RTP payload with a random IV.
//
// payload is the Opus TOC byte followed by the encoded frame. The returned
// frame is FrameOverhead bytes longer than payload. payload must not be
// empty; DTX frames should be skipped by the caller, matching LiveKit's JS
// implementation.
func Seal(payload, key []byte, kid uint8) ([]byte, error) {
	iv := make([]byte, IVSize)
	if _, err := rand.Read(iv); err != nil {
		return nil, fmt.Errorf("sframe: generate IV: %w", err)
	}
	return SealWithIV(payload, key, kid, iv)
}

// SealWithIV is Seal with a caller-supplied IV. It exists for known-answer
// tests and deterministic interop vectors; production callers should use
// Seal so the IV is fresh for every frame.
func SealWithIV(payload, key []byte, kid uint8, iv []byte) ([]byte, error) {
	if len(payload) == 0 {
		return nil, ErrEmptyFrame
	}
	if len(key) != AESKeySize {
		return nil, fmt.Errorf("%w: got %d", ErrKeySize, len(key))
	}
	if len(iv) != IVSize {
		return nil, fmt.Errorf("%w: got %d", ErrIVSize, len(iv))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("sframe: create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("sframe: create GCM: %w", err)
	}

	// Layout: TOC | ciphertext+tag | IV | IV length | KID
	out := make([]byte, 0, len(payload)+FrameOverhead)
	out = append(out, payload[0])
	out = gcm.Seal(out, iv, payload[opusTOCSize:], payload[:opusTOCSize])
	out = append(out, iv...)
	out = append(out, IVSize, kid)
	return out, nil
}

// Open decrypts a frame produced by Seal or by a LiveKit client.
//
// It reads the KID from the last byte and returns it alongside the plaintext,
// but it does not choose the key: callers decrypting inbound media should
// look the key up by the returned KID (KeyTable.Open does that). A frame from
// a different KID fails authentication with ErrDecrypt when that KID's key is
// used instead.
//
// The KID and IV-length bytes are not covered by the AEAD, exactly as in
// LiveKit's format. Tampering with them can change which key is selected or
// make the frame unparsable, but cannot change the decrypted plaintext.
//
// The returned plaintext starts with the cleartext Opus TOC byte.
func Open(frame, key []byte) ([]byte, uint8, error) {
	if len(key) != AESKeySize {
		return nil, 0, fmt.Errorf("%w: got %d", ErrKeySize, len(key))
	}
	if len(frame) < opusTOCSize+tagSize+trailerSize+1 {
		return nil, 0, ErrTruncatedFrame
	}

	kid := frame[len(frame)-1]
	ivLen := int(frame[len(frame)-trailerSize])
	if ivLen == 0 {
		return nil, 0, ErrIVLength
	}

	ivStart := len(frame) - trailerSize - ivLen
	ciphertextStart := opusTOCSize
	if ivStart <= ciphertextStart {
		return nil, 0, ErrIVLength
	}
	// A GCM ciphertext always carries a tag. Reject anything shorter before
	// asking the AEAD to authenticate it.
	if ivStart-ciphertextStart < tagSize {
		return nil, 0, ErrTruncatedFrame
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, 0, fmt.Errorf("sframe: create cipher: %w", err)
	}
	gcm, err := cipher.NewGCMWithNonceSize(block, ivLen)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %v", ErrIVLength, err)
	}

	plaintext, err := gcm.Open(nil, frame[ivStart:ivStart+ivLen], frame[ciphertextStart:ivStart], frame[:opusTOCSize])
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %v", ErrDecrypt, err)
	}

	// Reassemble TOC | plaintext.
	out := make([]byte, 0, opusTOCSize+len(plaintext))
	out = append(out, frame[:opusTOCSize]...)
	out = append(out, plaintext...)
	return out, kid, nil
}

// IsSIF reports whether frame is a plaintext server-injected frame (SIF).
//
// LiveKit's SFU injects a few unencrypted frames (for example, the "call
// ended" tone). They are recognisable by the trailer returned from
// lksdk's Room.SifTrailer and must be dropped before decryption: their last
// byte is part of the trailer, not a KID.
//
// An empty trailer disables detection, matching LiveKit's behaviour before
// the room has reported its trailer.
func IsSIF(frame, sifTrailer []byte) bool {
	if len(sifTrailer) == 0 || len(frame) < len(sifTrailer) {
		return false
	}
	return bytes.Equal(frame[len(frame)-len(sifTrailer):], sifTrailer)
}
