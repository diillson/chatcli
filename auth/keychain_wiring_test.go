/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package auth

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// fakeKeychain stands in for the OS store, so the wiring can be exercised
// on any platform without touching a real keychain.
type fakeKeychain struct {
	mu        sync.Mutex
	items     map[string][]byte
	available bool
	failSet   bool
	loseWrite bool  // accepts the write and returns nothing afterwards
	getErr    error // a backend that cannot answer (locked, access denied)
}

func newFakeKeychain() *fakeKeychain {
	return &fakeKeychain{items: map[string][]byte{}, available: true}
}

func (f *fakeKeychain) store(backend KeychainBackend) *KeychainStore {
	return &KeychainStore{backend: backend, ops: f}
}

func (f *fakeKeychain) Available() bool { return f.available }

func (f *fakeKeychain) Get(account string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, f.getErr
	}
	v, ok := f.items[account]
	if !ok {
		return nil, os.ErrNotExist
	}
	return v, nil
}

func (f *fakeKeychain) Set(account string, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
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
	f.mu.Lock()
	defer f.mu.Unlock()
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

// resetAdopt forgets which key paths this process already tried to adopt.
func resetAdopt(t *testing.T) {
	t.Helper()
	adoptMu.Lock()
	adoptDone = map[string]bool{}
	adoptMu.Unlock()
}

// sealStoreWith writes an auth-profiles.json next to keyPath encrypted with
// key, the way encryptData does.
func sealStoreWith(t *testing.T, keyPath string, key []byte) {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, gcm.NonceSize())
	sealed := gcm.Seal(nonce, nonce, []byte(`{"profiles":{}}`), nil)
	data := encryptedPrefix + base64.StdEncoding.EncodeToString(sealed)
	if err := os.WriteFile(filepath.Join(filepath.Dir(keyPath), "auth-profiles.json"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func encodedKey(key []byte) []byte { return []byte(base64.StdEncoding.EncodeToString(key)) }

// The default moves the key file into the keychain: verified read-back,
// then the file goes away.
func TestResolveKey_AutoAdoptsTheFileKeyIntoTheKeychain(t *testing.T) {
	resetAdopt(t)
	t.Setenv("CHATCLI_AUTH_DIR", "")
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
	if !bytes.Equal(fk.items[keychainAccount], encodedKey(want)) {
		t.Error("the file key was not moved into the keychain")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the key file is still on disk after the move")
	}
	again, err := resolveEncryptionKey(fk.store(KeychainAuto), path)
	if err != nil || !bytes.Equal(again, want) {
		t.Fatalf("the next resolve did not find the key in the keychain: %v", err)
	}
}

// A keychain holding the same key only costs the file.
func TestResolveKey_AutoSameKeyRemovesTheFile(t *testing.T) {
	resetAdopt(t)
	t.Setenv("CHATCLI_AUTH_DIR", "")
	path := keyPathIn(t)
	want := writeKey(t, path, 0x22)
	fk := newFakeKeychain()
	fk.items[keychainAccount] = encodedKey(want)

	if _, err := resolveEncryptionKey(fk.store(KeychainAuto), path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the key file is still on disk")
	}
}

// A different key in the keychain is never overwritten: another
// installation may depend on it. The file keeps being used.
func TestResolveKey_AutoDifferentKeyLeavesTheKeychainAlone(t *testing.T) {
	resetAdopt(t)
	t.Setenv("CHATCLI_AUTH_DIR", "")
	path := keyPathIn(t)
	fileKey := writeKey(t, path, 0x33)
	sealStoreWith(t, path, fileKey)
	other := bytes.Repeat([]byte{0x44}, 32)
	fk := newFakeKeychain()
	fk.items[keychainAccount] = encodedKey(other)

	got, err := resolveEncryptionKey(fk.store(KeychainAuto), path)
	if err != nil || !bytes.Equal(got, fileKey) {
		t.Fatalf("resolve = %v, want the file key as before", err)
	}
	if !bytes.Equal(fk.items[keychainAccount], encodedKey(other)) {
		t.Error("the keychain's key was overwritten")
	}
	if _, err := os.Stat(path); err != nil {
		t.Error("the key file was removed")
	}
	if keyIsInKeychain(path) {
		t.Error("the marker claims the key is in the keychain")
	}
}

// After the move, nothing creates a new key: not the file backend (a cron
// job), not a context without a keychain, not a keychain that answers "no
// such item". A new key would orphan every store sealed with the real one.
func TestResolveKey_AfterTheMoveNoNewKeyIsEverCreated(t *testing.T) {
	cases := map[string]func(fk *fakeKeychain) KeychainBackend{
		"file backend":      func(fk *fakeKeychain) KeychainBackend { return KeychainFile },
		"no keychain here":  func(fk *fakeKeychain) KeychainBackend { fk.available = false; return KeychainAuto },
		"item gone":         func(fk *fakeKeychain) KeychainBackend { delete(fk.items, keychainAccount); return KeychainAuto },
		"native, item gone": func(fk *fakeKeychain) KeychainBackend { delete(fk.items, keychainAccount); return KeychainNative },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			resetAdopt(t)
			t.Setenv("CHATCLI_AUTH_DIR", "")
			path := keyPathIn(t)
			writeKey(t, path, 0x55)
			fk := newFakeKeychain()
			if _, err := resolveEncryptionKey(fk.store(KeychainAuto), path); err != nil {
				t.Fatal(err)
			}
			if !keyIsInKeychain(path) {
				t.Fatal("the move did not leave its marker")
			}
			backend := setup(fk)
			before := len(fk.items)
			if _, err := resolveEncryptionKey(fk.store(backend), path); err == nil {
				t.Fatal("a new key was handed out after the key moved to the keychain")
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Error("a new key file was created")
			}
			if len(fk.items) > before {
				t.Error("a new key was written to the keychain")
			}
		})
	}
}

// A key file that shows up after the move (a restored backup, a container
// with the directory mounted) never replaces the keychain key the stores
// are sealed with.
func TestResolveKey_StrayFileAfterTheMoveUsesTheKeychainKey(t *testing.T) {
	resetAdopt(t)
	t.Setenv("CHATCLI_AUTH_DIR", "")
	path := keyPathIn(t)
	moved := writeKey(t, path, 0x66)
	fk := newFakeKeychain()
	if _, err := resolveEncryptionKey(fk.store(KeychainAuto), path); err != nil {
		t.Fatal(err)
	}
	writeKey(t, path, 0x77)
	resetAdopt(t)
	got, err := resolveEncryptionKey(fk.store(KeychainAuto), path)
	if err != nil || !bytes.Equal(got, moved) {
		t.Fatalf("resolve = %v, want the keychain key", err)
	}
	if !bytes.Equal(fk.items[keychainAccount], encodedKey(moved)) {
		t.Error("the stray file replaced the keychain key")
	}
	fk.getErr = os.ErrPermission
	if _, err := resolveEncryptionKey(fk.store(KeychainAuto), path); err == nil {
		t.Error("a locked keychain fell back to the stray file")
	}
}

// A keychain that cannot answer, a lost write, or a custom auth directory
// leave the file in place.
func TestResolveKey_AutoKeepsTheFileWhenTheMoveIsUnsafe(t *testing.T) {
	cases := map[string]func(fk *fakeKeychain, t *testing.T){
		"locked":     func(fk *fakeKeychain, t *testing.T) { fk.getErr = os.ErrPermission },
		"lost write": func(fk *fakeKeychain, t *testing.T) { fk.loseWrite = true },
		"set fails":  func(fk *fakeKeychain, t *testing.T) { fk.failSet = true },
		"custom dir": func(fk *fakeKeychain, t *testing.T) { t.Setenv("CHATCLI_AUTH_DIR", t.TempDir()) },
		"no keychain": func(fk *fakeKeychain, t *testing.T) {
			fk.available = false
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			resetAdopt(t)
			t.Setenv("CHATCLI_AUTH_DIR", "")
			path := keyPathIn(t)
			want := writeKey(t, path, 0x77)
			fk := newFakeKeychain()
			setup(fk, t)

			got, err := resolveEncryptionKey(fk.store(KeychainAuto), path)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("resolve = %v, want the file key", err)
			}
			if _, err := os.Stat(path); err != nil {
				t.Error("the key file was removed")
			}
		})
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

// Processes starting together on a machine with no key yet all end up
// with the same key: creation is serialized, and each one looks again
// under the lock before generating.
func TestResolveKey_ConcurrentFirstRunsAgreeOnOneKey(t *testing.T) {
	for _, withKeychain := range []bool{true, false} {
		path := keyPathIn(t)
		fk := newFakeKeychain()
		fk.available = withKeychain
		const n = 16
		keys := make([][]byte, n)
		errs := make([]error, n)
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				keys[i], errs[i] = resolveEncryptionKey(fk.store(KeychainAuto), path)
			}(i)
		}
		wg.Wait()
		for i := 0; i < n; i++ {
			if errs[i] != nil {
				t.Fatalf("keychain=%v: resolve %d: %v", withKeychain, i, errs[i])
			}
			if !bytes.Equal(keys[i], keys[0]) {
				t.Fatalf("keychain=%v: two first runs ended with different keys", withKeychain)
			}
		}
		again, err := resolveEncryptionKey(fk.store(KeychainAuto), path)
		if err != nil || !bytes.Equal(again, keys[0]) {
			t.Fatalf("keychain=%v: the stored key is not the one handed out", withKeychain)
		}
	}
}
