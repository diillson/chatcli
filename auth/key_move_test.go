/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package auth

import (
	"bytes"
	"errors"
	"os"
	"testing"
)

// The case the automatic move leaves alone: the keychain holds a different
// key. On request, a file key that opens the credential store moves in, the
// keychain's old value is kept, and the key itself does not change.
func TestMoveFileKeyToKeychain_ReplacesAStaleKeychainKeyOnRequest(t *testing.T) {
	resetAdopt(t)
	t.Setenv("CHATCLI_AUTH_DIR", "")
	path := keyPathIn(t)
	fileKey := writeKey(t, path, 0x71)
	sealStoreWith(t, path, fileKey)
	stale := bytes.Repeat([]byte{0x72}, 32)
	fk := newFakeKeychain()
	fk.items[keychainAccount] = encodedKey(stale)

	moved, err := moveKeyFileInto(fk.store(KeychainAuto), path, nil)
	if err != nil || !moved {
		t.Fatalf("move = %v, %v", moved, err)
	}
	if !bytes.Equal(fk.items[keychainAccount], encodedKey(fileKey)) {
		t.Error("the file key is not in the keychain")
	}
	if !bytes.Equal(fk.items[previousKeychainAccount], encodedKey(stale)) {
		t.Error("the keychain's previous value was not kept")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the key file is still on disk")
	}
	got, err := resolveEncryptionKey(fk.store(KeychainAuto), path)
	if err != nil || !bytes.Equal(got, fileKey) {
		t.Fatalf("the key changed after the move: %v", err)
	}
}

// A file key that does not open the credential store is not trusted over
// the keychain: nothing moves.
func TestMoveFileKeyToKeychain_RefusesAnUnprovenFile(t *testing.T) {
	resetAdopt(t)
	t.Setenv("CHATCLI_AUTH_DIR", "")
	path := keyPathIn(t)
	writeKey(t, path, 0x73)
	other := bytes.Repeat([]byte{0x74}, 32)
	sealStoreWith(t, path, other)
	fk := newFakeKeychain()
	fk.items[keychainAccount] = encodedKey(other)

	if _, err := moveKeyFileInto(fk.store(KeychainAuto), path, nil); !errors.Is(err, ErrKeyFileNotProven) {
		t.Fatalf("err = %v, want ErrKeyFileNotProven", err)
	}
	if !bytes.Equal(fk.items[keychainAccount], encodedKey(other)) {
		t.Error("the keychain changed")
	}
	if _, err := os.Stat(path); err != nil {
		t.Error("the key file was removed")
	}
}

func TestMoveFileKeyToKeychain_EdgeCases(t *testing.T) {
	resetAdopt(t)
	t.Setenv("CHATCLI_AUTH_DIR", "")

	// Same key on both sides: only the file goes.
	path := keyPathIn(t)
	key := writeKey(t, path, 0x75)
	fk := newFakeKeychain()
	fk.items[keychainAccount] = encodedKey(key)
	if moved, err := moveKeyFileInto(fk.store(KeychainAuto), path, nil); err != nil || !moved {
		t.Fatalf("same key: %v, %v", moved, err)
	}
	if _, ok := fk.items[previousKeychainAccount]; ok {
		t.Error("an identical key was backed up")
	}

	// No key file: nothing to do.
	if moved, err := moveKeyFileInto(newFakeKeychain().store(KeychainAuto), keyPathIn(t), nil); err != nil || moved {
		t.Fatalf("no file: %v, %v", moved, err)
	}

	// No keychain, or the file backend pinned.
	noKC := newFakeKeychain()
	noKC.available = false
	if _, err := moveKeyFileInto(noKC.store(KeychainAuto), keyPathIn(t), nil); !errors.Is(err, ErrKeychainNotUsable) {
		t.Fatalf("no keychain: %v", err)
	}
	if _, err := moveKeyFileInto(newFakeKeychain().store(KeychainFile), keyPathIn(t), nil); !errors.Is(err, ErrKeychainNotUsable) {
		t.Fatalf("file backend: %v", err)
	}

	// A keychain that cannot answer leaves the file alone.
	locked := newFakeKeychain()
	locked.getErr = os.ErrPermission
	lp := keyPathIn(t)
	writeKey(t, lp, 0x76)
	if _, err := moveKeyFileInto(locked.store(KeychainAuto), lp, nil); err == nil {
		t.Fatal("a locked keychain reported a move")
	}
	if _, err := os.Stat(lp); err != nil {
		t.Error("the key file was removed with a locked keychain")
	}

	// A custom auth directory never moves.
	t.Setenv("CHATCLI_AUTH_DIR", t.TempDir())
	if _, err := moveKeyFileInto(newFakeKeychain().store(KeychainAuto), keyPathIn(t), nil); !errors.Is(err, ErrCustomAuthDir) {
		t.Fatalf("custom dir: %v", err)
	}
}

func TestMoveFileKeyToKeychain_PublicEntryPoint(t *testing.T) {
	t.Setenv("CHATCLI_AUTH_DIR", t.TempDir())
	if _, err := MoveFileKeyToKeychain(nil); !errors.Is(err, ErrCustomAuthDir) {
		t.Fatalf("err = %v", err)
	}
}

// No credential store at all (never logged in): the sealed data is the
// proof. A file key whose data key does not open it never moves in.
func TestMoveFileKeyToKeychain_WithoutCredentialsTheDataIsTheProof(t *testing.T) {
	resetAdopt(t)
	t.Setenv("CHATCLI_AUTH_DIR", "")
	fk := newFakeKeychain()
	fk.items[keychainAccount] = encodedKey(bytes.Repeat([]byte{0x77}, 32))

	path := keyPathIn(t)
	fileKey := writeKey(t, path, 0x78)
	want, _ := deriveDataSecret(fileKey)
	var got string
	moved, err := moveKeyFileInto(fk.store(KeychainAuto), path, func(s string) bool { got = s; return false })
	if !errors.Is(err, ErrKeyFileNotProven) || moved {
		t.Fatalf("data that does not open: %v, %v", moved, err)
	}
	if got != want {
		t.Fatal("the data check did not receive the file key's data secret")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("the key file was removed")
	}
	if moved, err := moveKeyFileInto(fk.store(KeychainAuto), path, func(string) bool { return true }); err != nil || !moved {
		t.Fatalf("data that opens: %v, %v", moved, err)
	}
}
