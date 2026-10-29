// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package callcrypto

import (
	"encoding/json"
	"fmt"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"jmpchat/pkg/sframe"
)

// KeyEventType is the unstable to-device event carrying Element Call media keys (MSC4143). It is
// always sent wrapped in an Olm-encrypted m.room.encrypted to-device message.
var KeyEventType = event.Type{Type: "io.element.call.encryption_keys", Class: event.ToDeviceEventType}

// mediaKeyEvent is the decrypted payload of KeyEventType.
type mediaKeyEvent struct {
	RoomID   id.RoomID  `json:"room_id"`
	MemberID string     `json:"member_id"`
	MediaKey *keyIndex  `json:"media_key,omitempty"`
	Keys     *keyIndex  `json:"keys,omitempty"`
	Member   *memberRef `json:"member,omitempty"`
}

// keyIndex is one key of a sender's ring.
type keyIndex struct {
	Index uint32 `json:"index"`
	Key   string `json:"key"`
}

// memberRef names the sender device the payload claims to come from.
type memberRef struct {
	ClaimedDeviceID id.DeviceID `json:"claimed_device_id"`
	ID              string      `json:"id"`
}

// entry returns the media key entry (Element Call writes both media_key and keys).
func (k *mediaKeyEvent) entry() *keyIndex {
	if k.MediaKey != nil {
		return k.MediaKey
	}
	return k.Keys
}

// parseKeyEvent decodes and validates a decrypted key event. senderDevice is the device the Olm
// envelope came from; it must match the claimed_device_id of the payload, which is the only
// authenticity check Element Call itself does.
func parseKeyEvent(data []byte, senderDevice id.DeviceID) (*mediaKeyEvent, []byte, error) {
	var evt mediaKeyEvent
	if err := json.Unmarshal(data, &evt); err != nil {
		return nil, nil, fmt.Errorf("invalid key event payload: %w", err)
	}
	if evt.RoomID == "" {
		return nil, nil, fmt.Errorf("missing room_id")
	}
	if evt.Member == nil || evt.Member.ClaimedDeviceID == "" {
		return nil, nil, fmt.Errorf("missing member.claimed_device_id")
	}
	if senderDevice == "" {
		return nil, nil, fmt.Errorf("missing sender device in the Olm envelope")
	}
	if evt.Member.ClaimedDeviceID != senderDevice {
		return nil, nil, fmt.Errorf("claimed_device_id %q does not match the sender device %q", evt.Member.ClaimedDeviceID, senderDevice)
	}
	entry := evt.entry()
	if entry == nil {
		return nil, nil, fmt.Errorf("missing media key")
	}
	if entry.Index > uint32(sframe.MaxKID) {
		return nil, nil, fmt.Errorf("media key index %d out of range", entry.Index)
	}
	secret, err := sframe.DecodeSecretBase64(entry.Key)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid media key: %w", err)
	}
	return &evt, secret, nil
}
