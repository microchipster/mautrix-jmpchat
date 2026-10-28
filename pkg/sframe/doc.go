// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package sframe implements the media frame encryption format used by
// LiveKit clients and MatrixRTC (Element Call) calls.
//
// This is deliberately NOT RFC 9605 SFrame. LiveKit calls its format
// "similar to draft-omara-sframe-00, but we put the header at the end".
// An RFC 9605 implementation (chris-wood/sframe-go, cisco/sframe) produces
// frames that Element Call cannot read, and vice versa. The differences that
// matter are listed in docs/SFRAME_CALLS_M0_KEY_DISTRIBUTION.md §3.5.
//
// A sealed Opus payload looks like this:
//
//	+------------------+-------------------------------+------------+------+-----+
//	| Opus TOC (1 B)   | AES-128-GCM ciphertext + tag  | IV (12 B)  | 12   | KID |
//	| cleartext, AAD   | (plaintext_len - 1 + 16)      | cleartext  | 1 B  | 1 B |
//	+------------------+-------------------------------+------------+------+-----+
//
// The Opus table-of-contents byte is authenticated as additional data but
// travels in cleartext, so the receiver can inspect codec metadata without a
// key. The IV length byte is always 12 for frames this package produces;
// Open mirrors LiveKit's decryptor and accepts any non-zero IV length.
//
// Overhead is FrameOverhead (30) bytes per frame. Seal and Open operate on a
// single RTP payload packet (the RTP header stays outside the codec). Empty
// payloads are rejected: LiveKit clients skip DTX frames rather than
// encrypting them.
//
// Keys are not AES keys on the wire. The MatrixRTC to-device payload carries
// a 16-byte HKDF input secret; DeriveKey turns it into the AES-128 key
// (see §3.2 of the M0 document). KeyTable stores those secrets by KID and
// implements the KeyProvider interface expected by
// livekit/server-sdk-go/v2/e2ee/types, plus the rotation mechanics from §1.4.
//
// The wire format here must match lksdk's EncryptGCMAudioSampleCustomCipher /
// DecryptGCMAudioSampleCustomCipher (livekit/server-sdk-go/v2) byte for byte.
// The production media path may later switch to lksdk/e2ee with a KeyTable
// adapter; the package has no dependency on lksdk so that swap stays possible.
package sframe
