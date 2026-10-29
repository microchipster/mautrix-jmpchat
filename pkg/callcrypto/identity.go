// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package callcrypto

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"

	"maunium.net/go/mautrix/id"
)

// LegacyIdentity is the MSC4143 LiveKit identity of a member device: "<user>:<device>". It is the
// member id Element Call puts into the membership and the key event.
func LegacyIdentity(userID id.UserID, deviceID id.DeviceID) string {
	return userID.String() + ":" + deviceID.String()
}

// HashedIdentity is the MSC4195 identity: unpadded base64 of SHA-256 over the canonical JSON array
// of the member's user ID and member id. Newer clients publish (and subscribe to) track identities
// with that hash instead of the legacy string.
func HashedIdentity(userID id.UserID, memberID string) string {
	// json.Marshal of a two-element array of strings is canonical JSON: no spaces, no reordering.
	raw, _ := json.Marshal([]string{userID.String(), memberID})
	sum := sha256.Sum256(raw)
	return base64.RawStdEncoding.EncodeToString(sum[:])
}

// MemberIdentity returns every LiveKit identity a member device can appear as. Received keys are
// stored under all of them, so inbound frames decrypt whichever identity the SFU reports.
func MemberIdentity(userID id.UserID, deviceID id.DeviceID) []string {
	legacy := LegacyIdentity(userID, deviceID)
	return []string{legacy, HashedIdentity(userID, legacy)}
}
