/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package auth

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

// fakeKeychain stands in for the OS store, so the wiring can be exercised
// on any platform without touching a real keychain.
type fakeKeychain struct {
	items     map[string][]byte
	available bool
	failSet   bool
	loseWrite bool // accepts the write and returns nothing afterwards
}

func newFakeKeychain() *fakeKeychain {
	return &fakeKeychain{items: map[string][]byte{}, available: true}
}

func (f *fakeKeychain) store(backend KeychainBackend) *KeychainStore {
	return &KeychainStore{backend: backend, ops: f}
}

func (f *fakeKeychain) Available() bool { return f.available }

func (f *fakeKeychain) Get(account string) ([]byte, error) {
	v, ok := f.items[account]
	if !ok {
		return nil, os.ErrNotExist
	}
	return v, nil
}

func (f *fakeKeychain) Set(account string, data []byte) error {
	if f.failSet {
		return os.ErrPermission
	}
	if f.loseWrite {
		return nil
	}
	f.items[account] = append([]byte(nil), data...)
	return nil
}

func (f *fakeKeychain) Delete(account string) error {
	delete(f.items, account)
	return nil
}

func keyPathIn(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), ".auth-key")
}

func writeKey(t *testing.T, path string, b byte) []byte {
	t.Helper()
	key := bytes.Repeat([]byte{b}, 32)
	if err := os.WriteFile(path, key, 0o600); err != nil {
		t.Fatal(err)
	}
	return key
}

// The default must not move a working installation's key.
func TestResolveKey_AutoKeepsAnExistingFileKey(t *testing.T) {
	path := keyPathIn(t)
	want := writeKey(t, path, 0x11)
	fk := newFakeKeychain()

	got, err := resolveEncryptionKey(fk.store(KeychainAuto), path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Error("auto did not return the existing file key")
	}
	if len(fk.items) != 0 {
		t.Error("auto migrated a key into the keychain without being asked")
	}
	if _, err := os.Stat(path); err != nil {
		t.Error("auto removed the key file")
	}
}

// A first key on a machine with a keychain goes to the keychain, and no
// copy is left on disk.
func TestResolveKey_AutoPutsANewKeyInTheKeychain(t *testing.T) {
	path := keyPathIn(t)
	fk := newFakeKeychain()

	key, err := resolveEncryptionKey(fk.store(KeychainAuto), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(key) != 32 {
		t.Fatalf("key is %d bytes", len(key))
	}
	stored, ok := fk.items[keychainAccount]
	if !ok {
		t.Fatal("a new key was not stored in the keychain")
	}
	decoded, err := base64.StdEncoding.DecodeString(string(stored))
	if err != nil || !bytes.Equal(decoded, key) {
		t.Error("the stored key does not round-trip")
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("a copy of the key was left on disk")
	}
}

// With no keychain available, everything behaves exactly as before.
func TestResolveKey_FallsBackToFileWithoutAKeychain(t *testing.T) {
	path := keyPathIn(t)
	fk := newFakeKeychain()
	fk.available = false

	key, err := resolveEncryptionKey(fk.store(KeychainAuto), path)
	if err != nil {
		t.Fatal(err)
	}
	onDisk, ok := readKeyFile(path)
	if !ok || !bytes.Equal(onDisk, key) {
		t.Fatal("the key was not written to the file backend")
	}
}

func TestResolveKey_FileBackendNeverConsultsTheKeychain(t *testing.T) {
	path := keyPathIn(t)
	fk := newFakeKeychain()
	fk.items[keychainAccount] = []byte(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x22}, 32)))

	key, err := resolveEncryptionKey(fk.store(KeychainFile), path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(key, bytes.Repeat([]byte{0x22}, 32)) {
		t.Error("the file backend read from the keychain")
	}
	if _, ok := readKeyFile(path); !ok {
		t.Error("the file backend did not write a key file")
	}
}

// The explicit backend migrates, and only removes the file once the
// keychain has handed the key back.
func TestResolveKey_KeychainBackendMigratesThenRemovesTheFile(t *testing.T) {
	path := keyPathIn(t)
	want := writeKey(t, path, 0x33)
	fk := newFakeKeychain()

	got, err := resolveEncryptionKey(fk.store(KeychainNative), path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Error("migration changed the key")
	}
	if _, ok := fk.items[keychainAccount]; !ok {
		t.Fatal("the key was not migrated into the keychain")
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("the key file survived a verified migration")
	}
}

// A keychain that accepts a write and then returns nothing must not cost
// anyone their credentials.
func TestResolveKey_LostWriteKeepsTheFile(t *testing.T) {
	path := keyPathIn(t)
	want := writeKey(t, path, 0x44)
	fk := newFakeKeychain()
	fk.loseWrite = true

	got, err := resolveEncryptionKey(fk.store(KeychainNative), path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Error("the key changed")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("the key file was removed even though the keychain lost the write")
	}
}

func TestResolveKey_FailedWriteKeepsTheFile(t *testing.T) {
	path := keyPathIn(t)
	want := writeKey(t, path, 0x55)
	fk := newFakeKeychain()
	fk.failSet = true

	got, err := resolveEncryptionKey(fk.store(KeychainNative), path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Error("the key changed")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("the key file was removed after a failed keychain write")
	}
}

// An already-migrated installation reads from the keychain and needs no
// file at all.
func TestResolveKey_ReadsBackFromTheKeychain(t *testing.T) {
	path := keyPathIn(t)
	fk := newFakeKeychain()
	want := bytes.Repeat([]byte{0x66}, 32)
	fk.items[keychainAccount] = []byte(base64.StdEncoding.EncodeToString(want))

	for _, backend := range []KeychainBackend{KeychainNative, KeychainAuto} {
		got, err := resolveEncryptionKey(fk.store(backend), path)
		if err != nil {
			t.Fatalf("%s: %v", backend, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: did not read the key back from the keychain", backend)
		}
	}
}

// A stored value that is not a 32-byte key must not be handed to AES.
func TestResolveKey_IgnoresCorruptKeychainEntries(t *testing.T) {
	path := keyPathIn(t)
	for _, bad := range []string{"not-base64!!", base64.StdEncoding.EncodeToString([]byte("short")), ""} {
		fk := newFakeKeychain()
		fk.items[keychainAccount] = []byte(bad)
		if _, ok := keyFromKeychain(fk.store(KeychainAuto)); ok {
			t.Errorf("accepted a corrupt keychain entry %q", bad)
		}
	}
	// And the caller recovers by creating a usable key.
	fk := newFakeKeychain()
	fk.items[keychainAccount] = []byte("not-base64!!")
	key, err := resolveEncryptionKey(fk.store(KeychainAuto), path)
	if err != nil || len(key) != 32 {
		t.Fatalf("did not recover from a corrupt entry: key=%d err=%v", len(key), err)
	}
}

// --- store plumbing ---

// The store must route through its backend and report the file backend
// clearly rather than pretending to have a keychain.
func TestKeychainStore_RoutesThroughTheBackend(t *testing.T) {
	fk := newFakeKeychain()
	ks := fk.store(KeychainAuto)

	if err := ks.Set("acct", []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := ks.Get("acct")
	if err != nil || string(got) != "v" {
		t.Fatalf("Get = %q, %v", got, err)
	}
	if err := ks.Delete("acct"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := ks.Get("acct"); err == nil {
		t.Error("Get succeeded after Delete")
	}
}

func TestKeychainStore_FileBackendRefusesEveryOperation(t *testing.T) {
	fk := newFakeKeychain()
	ks := fk.store(KeychainFile)

	if ks.IsNativeAvailable() {
		t.Error("the file backend reported a native keychain")
	}
	if _, err := ks.Get("acct"); err == nil {
		t.Error("Get should say the file backend is in use")
	}
	if err := ks.Set("acct", []byte("v")); err == nil {
		t.Error("Set should say the file backend is in use")
	}
	if err := ks.Delete("acct"); err == nil {
		t.Error("Delete should say the file backend is in use")
	}
}

func TestKeychainStore_UnavailableKeychainIsNotUsed(t *testing.T) {
	fk := newFakeKeychain()
	fk.available = false
	ks := fk.store(KeychainAuto)

	if ks.IsNativeAvailable() {
		t.Error("an unavailable keychain reported itself available")
	}
	if _, err := ks.Get("acct"); err == nil {
		t.Error("Get used a keychain that is not there")
	}
}

func TestNewKeychainStore_ReadsTheBackendSetting(t *testing.T) {
	cases := map[string]KeychainBackend{
		"":         KeychainAuto,
		"auto":     KeychainAuto,
		"nonsense": KeychainAuto,
		"keychain": KeychainNative,
		"native":   KeychainNative,
		"KEYCHAIN": KeychainNative,
		"file":     KeychainFile,
		"FILE":     KeychainFile,
	}
	for value, want := range cases {
		t.Setenv("CHATCLI_KEYCHAIN_BACKEND", value)
		if got := NewKeychainStore().backend; got != want {
			t.Errorf("CHATCLI_KEYCHAIN_BACKEND=%q -> %q, want %q", value, got, want)
		}
	}
}

// The credential key round-trips through whichever backend resolved, which
// is the whole point of wiring the setting up.
func TestLoadOrCreateKey_RoundTripsThroughTheFileBackend(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CHATCLI_AUTH_DIR", dir)
	t.Setenv("CHATCLI_KEYCHAIN_BACKEND", "file")

	first, err := loadOrCreateKey()
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadOrCreateKey()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("a second call generated a different key")
	}

	cipherText, err := encryptData([]byte("secret-value"))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := decryptData(cipherText)
	if err != nil {
		t.Fatal(err)
	}
	if string(plain) != "secret-value" {
		t.Fatalf("round-trip returned %q", plain)
	}
}

func TestWriteKeyFile_UsesOwnerOnlyPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", ".auth-key")
	key := bytes.Repeat([]byte{0x77}, 32)
	if err := writeKeyFile(path, key); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("key file mode = %v, want 0600", perm)
	}
	got, ok := readKeyFile(path)
	if !ok || !bytes.Equal(got, key) {
		t.Error("the key did not read back")
	}
}

func TestNewRandomKey_IsAUsableAESKey(t *testing.T) {
	a, err := newRandomKey()
	if err != nil || len(a) != 32 {
		t.Fatalf("newRandomKey = %d bytes, %v", len(a), err)
	}
	b, err := newRandomKey()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) {
		t.Error("two calls produced the same key")
	}
}
