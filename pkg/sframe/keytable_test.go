// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sframe

import (
	"bytes"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

var testNow = time.Unix(1700000000, 0).UTC()

func newTestTable(t *testing.T) (*KeyTable, []byte) {
	t.Helper()
	secret := mustHex(t, "000102030405060708090a0b0c0d0e0f")
	table := NewKeyTable()
	if err := table.Insert(0, secret, testNow); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := table.Select(0); err != nil {
		t.Fatalf("Select: %v", err)
	}
	return table, secret
}

func TestKeyTableBasics(t *testing.T) {
	table := NewKeyTable()

	if table.Len() != 0 {
		t.Fatalf("empty Len = %d, want 0", table.Len())
	}
	if _, ok := table.Current(); ok {
		t.Fatal("empty table has a current key")
	}
	if _, err := table.GetKey(0); !errors.Is(err, ErrNoKey) {
		t.Fatalf("GetKey on empty table = %v, want ErrNoKey", err)
	}
	if _, err := table.Seal([]byte{0xf8}); !errors.Is(err, ErrNoCurrentKey) {
		t.Fatalf("Seal without key = %v, want ErrNoCurrentKey", err)
	}

	table, secret := newTestTable(t)
	if table.Len() != 1 {
		t.Fatalf("Len = %d, want 1", table.Len())
	}

	current, ok := table.Current()
	if !ok {
		t.Fatal("Current = false")
	}
	if current.KID != 0 {
		t.Fatalf("current KID = %d, want 0", current.KID)
	}
	if !bytes.Equal(current.Secret, secret) {
		t.Fatalf("current secret = %x, want %x", current.Secret, secret)
	}
	if !bytes.Equal(current.AESKey, mustHex(t, "4086b4641064e1ae8b63d4eb83ad3e7e")) {
		t.Fatalf("current AES key = %x", current.AESKey)
	}
	if !current.ReceivedAt.Equal(testNow) {
		t.Fatalf("ReceivedAt = %v, want %v", current.ReceivedAt, testNow)
	}
	if table.CurrentKeyIndex() != 0 {
		t.Fatalf("CurrentKeyIndex = %d, want 0", table.CurrentKeyIndex())
	}
	if !bytes.Equal(table.KIDs(), []uint8{0}) {
		t.Fatalf("KIDs = %v, want [0]", table.KIDs())
	}
}

func TestKeyTableOverwrite(t *testing.T) {
	table, _ := newTestTable(t)
	other := mustHex(t, "101112131415161718191a1b1c1d1e1f")

	oldKey, err := table.GetKey(0)
	if err != nil {
		t.Fatalf("GetKey: %v", err)
	}
	oldKeyCopy := append([]byte(nil), oldKey...)

	if err := table.Insert(0, other, testNow.Add(time.Second)); err != nil {
		t.Fatalf("Insert overwrite: %v", err)
	}
	if table.Len() != 1 {
		t.Fatalf("Len after overwrite = %d, want 1", table.Len())
	}
	newKey, err := table.GetKey(0)
	if err != nil {
		t.Fatalf("GetKey: %v", err)
	}
	if bytes.Equal(newKey, oldKeyCopy) {
		t.Fatal("overwrite did not replace key material")
	}

	// An AES key handed out before the overwrite must stay valid, because
	// entries are immutable. This is what lets a decryptor keep using a key
	// it fetched while a rotation happens.
	if _, _, err := Open(mustHex(t, "f83524ddffa627b64c723a6827f452b6815aa11ba7a509b49b16fa6bab7ec5138f36fa77"+
		"000102030405060708090a0b0c07"), oldKeyCopy); err != nil {
		t.Fatalf("old key stopped working after overwrite: %v", err)
	}
}

func TestKeyTableSnapshotsAreCopies(t *testing.T) {
	table, secret := newTestTable(t)

	snapshot, ok := table.Lookup(0)
	if !ok {
		t.Fatal("Lookup = false")
	}
	snapshot.Secret[0] ^= 0xff
	snapshot.AESKey[0] ^= 0xff

	again, ok := table.Lookup(0)
	if !ok {
		t.Fatal("Lookup = false")
	}
	if !bytes.Equal(again.Secret, secret) {
		t.Fatal("mutating a snapshot changed the stored secret")
	}
	if again.AESKey[0] == snapshot.AESKey[0] {
		t.Fatal("mutating a snapshot changed the stored AES key")
	}

	// GetKey deliberately returns the internal slice; the caller must not
	// mutate it. Just check the documented behaviour: it is the derived key.
	key, err := table.GetKey(0)
	if err != nil {
		t.Fatalf("GetKey: %v", err)
	}
	if !bytes.Equal(key, again.AESKey) {
		t.Fatal("GetKey does not return the derived AES key")
	}
}

func TestKeyTableGetKeyRange(t *testing.T) {
	table, _ := newTestTable(t)

	if _, err := table.GetKey(256); !errors.Is(err, ErrKIDRange) {
		t.Fatalf("GetKey(256) = %v, want ErrKIDRange", err)
	}
	if _, err := table.GetKey(255); !errors.Is(err, ErrNoKey) {
		t.Fatalf("GetKey(255) = %v, want ErrNoKey", err)
	}
	if err := table.Select(255); !errors.Is(err, ErrNoKey) {
		t.Fatalf("Select(255) = %v, want ErrNoKey", err)
	}
}

func TestKeyTableSealOpen(t *testing.T) {
	table, _ := newTestTable(t)
	payload := mustHex(t, "f80102030405060708090a0b0c0d0e0f10111213")

	frame, err := table.Seal(payload)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if frame[len(frame)-1] != 0 {
		t.Fatalf("frame KID = %d, want 0", frame[len(frame)-1])
	}

	plaintext, err := table.Open(frame, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(plaintext, payload) {
		t.Fatalf("plaintext = %x, want %x", plaintext, payload)
	}
}

func TestKeyTableOpenWrongKID(t *testing.T) {
	table, _ := newTestTable(t)
	if err := table.Insert(1, mustHex(t, "101112131415161718191a1b1c1d1e1f"), testNow); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	payload := []byte{0xf8, 0x01, 0x02}
	frame, err := table.Seal(payload)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	// The frame is sealed with KID 0. Decrypting it with key 1 must fail.
	wrongKey, err := table.GetKey(1)
	if err != nil {
		t.Fatalf("GetKey(1): %v", err)
	}
	if _, _, err := Open(frame, wrongKey); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("wrong-KID decrypt = %v, want ErrDecrypt", err)
	}

	// Rewriting the KID picks a different key in the table too.
	wrongKID := append([]byte(nil), frame...)
	wrongKID[len(wrongKID)-1] = 1
	if _, err := table.Open(wrongKID, nil); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("table Open with rewritten KID = %v, want ErrDecrypt", err)
	}

	unknownKID := append([]byte(nil), frame...)
	unknownKID[len(unknownKID)-1] = 5
	if _, err := table.Open(unknownKID, nil); !errors.Is(err, ErrNoKey) {
		t.Fatalf("table Open with unknown KID = %v, want ErrNoKey", err)
	}

	// The original still decrypts.
	if _, err := table.Open(frame, nil); err != nil {
		t.Fatalf("Open after failed attempts: %v", err)
	}
}

func TestKeyTableSIF(t *testing.T) {
	table, _ := newTestTable(t)
	trailer := mustHex(t, "deadbeefcafe")
	frame := append([]byte{0xf8, 0x01, 0x02}, trailer...)

	if _, err := table.Open(frame, trailer); !errors.Is(err, ErrSIF) {
		t.Fatalf("Open(SIF) = %v, want ErrSIF", err)
	}
	if _, err := table.Open(frame, nil); err == nil {
		t.Fatal("Open without trailer accepted a SIF")
	}
}

func TestKeyTableRotationMechanics(t *testing.T) {
	table, _ := newTestTable(t)
	nextSecret := mustHex(t, "101112131415161718191a1b1c1d1e1f")

	kid, err := table.Rotate(nextSecret, testNow)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if kid != 1 {
		t.Fatalf("rotated KID = %d, want 1", kid)
	}

	// The old key stays current until the new one has been distributed and
	// the use delay has passed.
	current, ok := table.Current()
	if !ok || current.KID != 0 {
		t.Fatalf("current after Rotate = %+v, want KID 0", current)
	}
	if table.CurrentKeyIndex() != 0 {
		t.Fatalf("CurrentKeyIndex after Rotate = %d, want 0", table.CurrentKeyIndex())
	}

	pending, ok := table.Pending()
	if !ok {
		t.Fatal("Pending = false after Rotate")
	}
	if pending.KID != 1 {
		t.Fatalf("pending KID = %d, want 1", pending.KID)
	}
	if want := testNow.Add(KeyUseDelay); !pending.ReadyAt.Equal(want) {
		t.Fatalf("pending ReadyAt = %v, want %v", pending.ReadyAt, want)
	}

	// The new key must already be usable for decryption: a peer can start
	// sending with it as soon as it has been distributed.
	if _, err := table.GetKey(1); err != nil {
		t.Fatalf("GetKey(1) immediately after Rotate: %v", err)
	}

	if kid, ok := table.PromotePending(testNow.Add(KeyUseDelay - time.Nanosecond)); ok {
		t.Fatalf("PromotePending promoted %d before the delay", kid)
	}
	kid, ok = table.PromotePending(testNow.Add(KeyUseDelay))
	if !ok || kid != 1 {
		t.Fatalf("PromotePending = (%d, %v), want (1, true)", kid, ok)
	}
	if table.CurrentKeyIndex() != 1 {
		t.Fatalf("CurrentKeyIndex = %d, want 1", table.CurrentKeyIndex())
	}
	if _, ok := table.Pending(); ok {
		t.Fatal("Pending still set after promotion")
	}

	// Frames sealed before the rotation still decrypt with the old key.
	if _, err := table.GetKey(0); err != nil {
		t.Fatalf("old key removed by rotation: %v", err)
	}

	frame, err := table.Seal([]byte{0xf8, 0x01})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if frame[len(frame)-1] != 1 {
		t.Fatalf("sealed frame KID = %d, want 1", frame[len(frame)-1])
	}
}

func TestKeyTableFirstRotationUsesIndexZero(t *testing.T) {
	table := NewKeyTable()
	secret := mustHex(t, "000102030405060708090a0b0c0d0e0f")

	kid, err := table.Rotate(secret, testNow)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if kid != 0 {
		t.Fatalf("first rotated KID = %d, want 0", kid)
	}
	if _, err := table.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if table.CurrentKeyIndex() != 0 {
		t.Fatalf("CurrentKeyIndex = %d, want 0", table.CurrentKeyIndex())
	}
}

func TestKeyTableRotationWrap(t *testing.T) {
	table := NewKeyTable()
	secret := mustHex(t, "000102030405060708090a0b0c0d0e0f")
	if err := table.Insert(255, secret, testNow); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := table.Select(255); err != nil {
		t.Fatalf("Select: %v", err)
	}

	kid, err := table.Rotate(secret, testNow)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if kid != 0 {
		t.Fatalf("rotated KID = %d, want 0 (wrap)", kid)
	}
	if _, ok := table.Pending(); !ok {
		t.Fatal("wrapped key is not pending")
	}
}

func TestKeyTableBackToBackRotation(t *testing.T) {
	table, _ := newTestTable(t)
	secret := mustHex(t, "101112131415161718191a1b1c1d1e1f")

	first, err := table.Rotate(secret, testNow)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	second, err := table.Rotate(secret, testNow.Add(time.Millisecond))
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if first != 1 || second != 2 {
		t.Fatalf("rotated KIDs = (%d, %d), want (1, 2)", first, second)
	}
	if _, err := table.GetKey(uint32(first)); err != nil {
		t.Fatalf("first rotated key was overwritten: %v", err)
	}
	pending, ok := table.Pending()
	if !ok || pending.KID != 2 {
		t.Fatalf("pending = %+v, want KID 2", pending)
	}
}

func TestKeyTableActivate(t *testing.T) {
	table, _ := newTestTable(t)
	secret := mustHex(t, "101112131415161718191a1b1c1d1e1f")

	if _, err := table.Activate(); !errors.Is(err, ErrNoPendingKey) {
		t.Fatalf("Activate without pending = %v, want ErrNoPendingKey", err)
	}

	if _, err := table.Rotate(secret, testNow); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	kid, err := table.Activate()
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if kid != 1 {
		t.Fatalf("Activate = %d, want 1", kid)
	}
	if _, ok := table.Pending(); ok {
		t.Fatal("Pending still set after Activate")
	}
}

func TestKeyTableRemoveAndReset(t *testing.T) {
	table, _ := newTestTable(t)
	if err := table.Insert(1, mustHex(t, "101112131415161718191a1b1c1d1e1f"), testNow); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	table.Remove(1)
	if _, ok := table.Lookup(1); ok {
		t.Fatal("removed key still present")
	}
	if table.Len() != 1 {
		t.Fatalf("Len = %d, want 1", table.Len())
	}

	table.Remove(0)
	if table.Len() != 0 {
		t.Fatalf("Len after removing current = %d, want 0", table.Len())
	}
	if _, ok := table.Current(); ok {
		t.Fatal("removing the current key left it selected")
	}
	if _, err := table.Seal([]byte{0xf8}); !errors.Is(err, ErrNoCurrentKey) {
		t.Fatalf("Seal after removing current = %v, want ErrNoCurrentKey", err)
	}

	table, _ = newTestTable(t)
	if err := table.Insert(1, mustHex(t, "101112131415161718191a1b1c1d1e1f"), testNow); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	table.Reset()
	if table.Len() != 0 || len(table.KIDs()) != 0 {
		t.Fatalf("Reset left %d keys", table.Len())
	}
	if _, ok := table.Current(); ok {
		t.Fatal("Reset left a current key")
	}
	if _, ok := table.Pending(); ok {
		t.Fatal("Reset left a pending key")
	}
}

func TestKeyTableRemovePending(t *testing.T) {
	table, _ := newTestTable(t)
	secret := mustHex(t, "101112131415161718191a1b1c1d1e1f")
	kid, err := table.Rotate(secret, testNow)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	table.Remove(kid)
	if _, ok := table.Pending(); ok {
		t.Fatal("removing a pending key left it selected")
	}
	if table.CurrentKeyIndex() != 0 {
		t.Fatalf("CurrentKeyIndex = %d, want 0", table.CurrentKeyIndex())
	}
}

func TestDecideJoin(t *testing.T) {
	tests := []struct {
		name string
		age  time.Duration
		want JoinAction
	}{
		{name: "brand new key", age: 0, want: JoinShareCurrent},
		{name: "just inside grace", age: JoinReuseWindow - time.Nanosecond, want: JoinShareCurrent},
		{name: "exactly at grace", age: JoinReuseWindow, want: JoinRotate},
		{name: "outside grace", age: JoinReuseWindow + time.Second, want: JoinRotate},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := DecideJoin(test.age); got != test.want {
				t.Fatalf("DecideJoin(%v) = %v, want %v", test.age, got, test.want)
			}
		})
	}

	if JoinReuseWindow != 10*time.Second {
		t.Fatalf("JoinReuseWindow = %v, want 10s", JoinReuseWindow)
	}
	if KeyUseDelay != time.Second {
		t.Fatalf("KeyUseDelay = %v, want 1s", KeyUseDelay)
	}
}

func TestKeyTableConcurrentUse(t *testing.T) {
	table, _ := newTestTable(t)

	const (
		workers    = 8
		iterations = 200
	)
	errs := make(chan error, workers*iterations)

	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			payload := []byte{0xf8, byte(worker), 0x11, 0x22}
			for i := 0; i < iterations; i++ {
				frame, err := table.Seal(payload)
				if err != nil {
					errs <- fmt.Errorf("Seal: %w", err)
					return
				}
				plaintext, err := table.Open(frame, nil)
				if err != nil {
					errs <- fmt.Errorf("Open: %w", err)
					return
				}
				if !bytes.Equal(plaintext, payload) {
					errs <- errors.New("round trip mismatch")
					return
				}
			}
		}(worker)
	}

	// Rotate and promote while the workers encrypt, staying well below the
	// 256-entry wrap so no key in flight is overwritten.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			if _, err := table.Rotate([]byte{byte(i), 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}, testNow); err != nil {
				errs <- fmt.Errorf("Rotate: %w", err)
				return
			}
			if _, err := table.Activate(); err != nil {
				errs <- fmt.Errorf("Activate: %w", err)
				return
			}
		}
	}()

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}
