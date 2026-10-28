// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sframe

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

const (
	// MaxKID is the largest key index the one-byte KID can carry.
	MaxKID uint8 = 255

	// keySlots is the number of KID slots in a table.
	keySlots = int(MaxKID) + 1

	// JoinReuseWindow is how long an existing key may be reused when a new
	// participant joins. Inside the window the key is shared with the joiner
	// only; outside it, everyone gets a fresh key
	// (RTCEncryptionManager.ts:106,268).
	JoinReuseWindow = 10 * time.Second

	// KeyUseDelay is how long a newly distributed key stays pending before
	// the sender starts using it. Element Call uses 1 s; the JS constructor
	// default is 5 s. The old key remains available for decryption
	// throughout (RTCEncryptionManager.ts:96,267). Callers drive the clock
	// and call PromotePending.
	KeyUseDelay = 1 * time.Second
)

var (
	// ErrNoKey is returned when a KID has no key in the table.
	ErrNoKey = errors.New("sframe: no key for KID")

	// ErrKIDRange is returned when a KID does not fit in one byte.
	ErrKIDRange = errors.New("sframe: KID out of range")

	// ErrNoCurrentKey is returned when sealing without a selected key.
	ErrNoCurrentKey = errors.New("sframe: no current key")

	// ErrNoPendingKey is returned when there is no pending rotation.
	ErrNoPendingKey = errors.New("sframe: no pending key")
)

// KeyProvider is the interface livekit/server-sdk-go/v2/e2ee/types expects.
// KeyTable implements it; callers can use a table with lksdk.NewFrameEncryptor
// and lksdk.NewFrameDecryptor without an adapter.
type KeyProvider interface {
	// GetKey returns the derived AES key for a KID. The returned slice must
	// not be modified.
	GetKey(keyIndex uint32) ([]byte, error)
	// CurrentKeyIndex returns the KID used for outbound encryption.
	CurrentKeyIndex() uint32
}

var _ KeyProvider = (*KeyTable)(nil)

// Key is a snapshot of one table entry. Secret and AESKey are copies and may
// be retained by the caller.
type Key struct {
	// KID is the key index, 0..MaxKID.
	KID uint8
	// Secret is the original HKDF input material from the to-device event.
	Secret []byte
	// AESKey is DeriveKey(Secret).
	AESKey []byte
	// ReceivedAt is when Insert stored the entry.
	ReceivedAt time.Time
	// ReadyAt is when a pending rotated key should start being used. It is
	// zero for keys inserted with Insert and for the current key.
	ReadyAt time.Time
}

// keyEntry is immutable after insertion, so readers may hold the pointer
// after releasing the lock.
type keyEntry struct {
	secret     []byte
	aesKey     []byte
	receivedAt time.Time
	readyAt    time.Time
}

func newKeyEntry(secret []byte, now time.Time) (*keyEntry, error) {
	// Copy before calling DeriveKey so a caller reusing its buffer cannot
	// change stored material later.
	secretCopy := append([]byte(nil), secret...)
	aesKey, err := DeriveKey(secretCopy)
	if err != nil {
		return nil, err
	}
	return &keyEntry{
		secret:     secretCopy,
		aesKey:     aesKey,
		receivedAt: now,
	}, nil
}

// KeyTable stores per-sender frame keys indexed by KID and tracks which key is
// used for outbound encryption. It is safe for concurrent use.
//
// A table belongs to one peer: per room and LiveKit identity, as inbound
// frames are encrypted with the sender's key. M2 creates one table per
// identity and feeds it the keys received over to-device messages.
//
// Inserting a KID that already exists overwrites the slot. That matches
// Element Call's OutdatedKeyFilter semantics closely enough for a receiver:
// a newer key at the same index wins (M0 §1.4).
type KeyTable struct {
	mu         sync.RWMutex
	keys       [keySlots]*keyEntry
	count      int
	current    uint8
	hasCurrent bool
	pending    uint8
	hasPending bool
}

// NewKeyTable returns an empty table with no current key.
func NewKeyTable() *KeyTable {
	return &KeyTable{}
}

// Insert stores secret at kid and derives its AES key. now is recorded as the
// receipt time. An existing key at the same KID is replaced.
func (t *KeyTable) Insert(kid uint8, secret []byte, now time.Time) error {
	entry, err := newKeyEntry(secret, now)
	if err != nil {
		return err
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.keys[kid] == nil {
		t.count++
	}
	t.keys[kid] = entry
	return nil
}

// Select makes an existing KID the current outbound key immediately. Use it
// for the initial key, which Element Call uses as soon as it is distributed.
// For rotations, prefer Rotate plus PromotePending so the 1 s delay applies.
func (t *KeyTable) Select(kid uint8) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.keys[kid] == nil {
		return fmt.Errorf("%w: %d", ErrNoKey, kid)
	}
	t.current = kid
	t.hasCurrent = true
	if t.hasPending && t.pending == kid {
		t.hasPending = false
	}
	return nil
}

// Rotate inserts secret at the next KID and marks it pending. It does not
// change the current key: distribute the new key first, then call
// PromotePending once KeyUseDelay has elapsed.
//
// The next index is (current+1) mod 256, or (pending+1) mod 256 if an earlier
// rotation is still waiting, so back-to-back rotations cannot overwrite each
// other. On an empty table the first key gets index 0. The old key stays in
// the table, so frames still in flight decrypt.
//
// The returned KID is the index of the new key.
func (t *KeyTable) Rotate(secret []byte, now time.Time) (uint8, error) {
	entry, err := newKeyEntry(secret, now)
	if err != nil {
		return 0, err
	}
	entry.readyAt = now.Add(KeyUseDelay)

	t.mu.Lock()
	defer t.mu.Unlock()

	var kid uint8
	switch {
	case t.hasPending:
		kid = t.pending + 1 // uint8 wraps 255 -> 0
	case t.hasCurrent:
		kid = t.current + 1
	default:
		kid = 0
	}

	if t.keys[kid] == nil {
		t.count++
	}
	t.keys[kid] = entry
	t.pending = kid
	t.hasPending = true
	return kid, nil
}

// PromotePending selects the pending key if now is at or past its ReadyAt.
// It reports whether a promotion happened. Callers drive timing: run it after
// KeyUseDelay, or lazily before each encryption.
func (t *KeyTable) PromotePending(now time.Time) (uint8, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.hasPending {
		return 0, false
	}
	entry := t.keys[t.pending]
	if entry == nil {
		// The pending slot was removed underneath us.
		t.hasPending = false
		return 0, false
	}
	if now.Before(entry.readyAt) {
		return 0, false
	}
	kid := t.pending
	t.current = kid
	t.hasCurrent = true
	t.hasPending = false
	return kid, true
}

// Activate forces the pending key to become current, ignoring
// KeyUseDelay. It is useful in tests and when a caller has already waited.
func (t *KeyTable) Activate() (uint8, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.hasPending {
		return 0, ErrNoPendingKey
	}
	kid := t.pending
	t.current = kid
	t.hasCurrent = true
	t.hasPending = false
	return kid, nil
}

// Remove deletes a key. It clears the current/pending selection if the
// removed KID held it.
func (t *KeyTable) Remove(kid uint8) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.keys[kid] == nil {
		return
	}
	t.keys[kid] = nil
	t.count--
	if t.hasCurrent && t.current == kid {
		t.hasCurrent = false
	}
	if t.hasPending && t.pending == kid {
		t.hasPending = false
	}
}

// Reset removes every key and clears both selections.
func (t *KeyTable) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for kid := range t.keys {
		t.keys[kid] = nil
	}
	t.count = 0
	t.hasCurrent = false
	t.hasPending = false
	t.current = 0
	t.pending = 0
}

// Len returns the number of stored keys.
func (t *KeyTable) Len() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.count
}

// KIDs returns the stored KIDs in ascending order.
func (t *KeyTable) KIDs() []uint8 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	kids := make([]uint8, 0, t.count)
	for kid, entry := range t.keys {
		if entry != nil {
			kids = append(kids, uint8(kid))
		}
	}
	sort.Slice(kids, func(i, j int) bool { return kids[i] < kids[j] })
	return kids
}

// Lookup returns a snapshot of the key at kid, including its secret.
func (t *KeyTable) Lookup(kid uint8) (Key, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.snapshot(kid), t.keys[kid] != nil
}

// Current returns a snapshot of the current outbound key.
func (t *KeyTable) Current() (Key, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if !t.hasCurrent {
		return Key{}, false
	}
	return t.snapshot(t.current), true
}

// CurrentKeyIndex returns the current outbound KID, or 0 when none is
// selected. The bool-less signature is required by the LiveKit KeyProvider
// interface; use Current to distinguish "no key" from KID 0.
func (t *KeyTable) CurrentKeyIndex() uint32 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return uint32(t.current)
}

// Pending returns a snapshot of the key waiting to become current.
func (t *KeyTable) Pending() (Key, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if !t.hasPending {
		return Key{}, false
	}
	return t.snapshot(t.pending), true
}

// GetKey returns the derived AES key for keyIndex. The slice is owned by the
// table and must not be modified; this keeps the per-frame path allocation
// free. Unlike CurrentKeyIndex, it rejects indices above MaxKID.
func (t *KeyTable) GetKey(keyIndex uint32) ([]byte, error) {
	if keyIndex > uint32(MaxKID) {
		return nil, fmt.Errorf("%w: %d", ErrKIDRange, keyIndex)
	}
	kid := uint8(keyIndex)

	t.mu.RLock()
	entry := t.keys[kid]
	t.mu.RUnlock()
	if entry == nil {
		return nil, fmt.Errorf("%w: %d", ErrNoKey, kid)
	}
	return entry.aesKey, nil
}

// Seal encrypts payload with the current key and its KID. It does not promote
// a pending rotation: callers call PromotePending (or Activate) when they
// decide the new key's KeyUseDelay has elapsed.
func (t *KeyTable) Seal(payload []byte) ([]byte, error) {
	t.mu.RLock()
	if !t.hasCurrent {
		t.mu.RUnlock()
		return nil, ErrNoCurrentKey
	}
	kid := t.current
	key := t.keys[kid].aesKey
	t.mu.RUnlock()

	return Seal(payload, key, kid)
}

// Open decrypts an inbound frame with the key selected by its KID. sifTrailer
// is the value from lksdk's Room.SifTrailer; frames matching it are plaintext
// and return ErrSIF so the caller can drop them. Pass nil to disable
// detection.
func (t *KeyTable) Open(frame, sifTrailer []byte) ([]byte, error) {
	if IsSIF(frame, sifTrailer) {
		return nil, ErrSIF
	}
	if len(frame) < trailerSize {
		return nil, ErrTruncatedFrame
	}

	kid := frame[len(frame)-1]
	key, err := t.GetKey(uint32(kid))
	if err != nil {
		return nil, err
	}
	plaintext, _, err := Open(frame, key)
	return plaintext, err
}

func (t *KeyTable) snapshot(kid uint8) Key {
	entry := t.keys[kid]
	if entry == nil {
		return Key{}
	}
	return Key{
		KID:        kid,
		Secret:     append([]byte(nil), entry.secret...),
		AESKey:     append([]byte(nil), entry.aesKey...),
		ReceivedAt: entry.receivedAt,
		ReadyAt:    entry.readyAt,
	}
}

// JoinAction describes what to do when a participant joins, per M0 §1.4.
type JoinAction int

const (
	// JoinShareCurrent means the key is younger than JoinReuseWindow: share
	// the current key with the joiner only.
	JoinShareCurrent JoinAction = iota
	// JoinRotate means the key is at least JoinReuseWindow old: distribute a
	// fresh key to every participant.
	JoinRotate
)

// DecideJoin implements the join policy from Element Call's
// onMembershipsUpdate: reuse the current key while it is younger than
// JoinReuseWindow, otherwise rotate. Callers measure key age themselves (the
// entry's ReceivedAt is a good source) and perform the distribution; this
// helper only encodes the threshold.
//
// Leaving participants always warrant a rotation, and there is no periodic
// rotation in the reference implementation; callers should not invent one.
func DecideJoin(keyAge time.Duration) JoinAction {
	if keyAge < JoinReuseWindow {
		return JoinShareCurrent
	}
	return JoinRotate
}
