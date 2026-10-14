/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package auth

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// A keychain that cannot answer (locked, access denied) must not be read as
// "no key yet": minting then would replace the key it holds and orphan
// every stored credential.
func TestResolveKey_UnreadableKeychainNeverMintsAKey(t *testing.T) {
	for _, backend := range []KeychainBackend{KeychainAuto, KeychainNative} {
		path := keyPathIn(t)
		fk := newFakeKeychain()
		held := []byte(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x5a}, 32)))
		fk.items[keychainAccount] = held
		fk.getErr = errors.New("user interaction is not allowed")

		key, err := resolveEncryptionKey(fk.store(backend), path)
		if err == nil || key != nil {
			t.Fatalf("%s: an unreadable keychain resolved a key (%d bytes, err=%v)", backend, len(key), err)
		}
		if !strings.Contains(err.Error(), keychainEnvVar) {
			t.Errorf("%s: the error does not say what to set: %v", backend, err)
		}
		if !bytes.Equal(fk.items[keychainAccount], held) {
			t.Errorf("%s: the keychain item was replaced", backend)
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s: a key file was created: %v", backend, err)
		}
	}
}

// A key file that exists but is not a key is left alone, not replaced.
func TestResolveKey_MalformedKeyFileIsNotReplaced(t *testing.T) {
	path := keyPathIn(t)
	if err := os.WriteFile(path, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveEncryptionKey(newFakeKeychain().store(KeychainFile), path); err == nil {
		t.Fatal("a malformed key file resolved to a key")
	}
	if got, _ := os.ReadFile(path); string(got) != "short" {
		t.Fatalf("the key file was rewritten: %q", got)
	}
}

// createKeyFile never overwrites: the loser of a first-run race reads the
// winner's key back.
func TestCreateKeyFile_ExclusiveAndRereadsTheWinner(t *testing.T) {
	path := keyPathIn(t)
	winner := writeKey(t, path, 0x0a)
	got, err := createKeyFile(path, bytes.Repeat([]byte{0x0b}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, winner) {
		t.Fatal("createKeyFile did not return the key already on disk")
	}
	if onDisk, _ := readKeyFile(path); !bytes.Equal(onDisk, winner) {
		t.Fatal("createKeyFile overwrote an existing key")
	}
}

func TestResolveKey_ConcurrentFirstRunAgreesOnOneKey(t *testing.T) {
	path := keyPathIn(t)
	const n = 16
	keys := make([][]byte, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			keys[i], errs[i] = resolveEncryptionKey(&KeychainStore{backend: KeychainFile}, path)
		}(i)
	}
	wg.Wait()
	for i := range keys {
		if errs[i] != nil {
			t.Fatalf("process %d: %v", i, errs[i])
		}
		if !bytes.Equal(keys[i], keys[0]) {
			t.Fatalf("process %d resolved a different key", i)
		}
	}
	if onDisk, _ := readKeyFile(path); !bytes.Equal(onDisk, keys[0]) {
		t.Fatal("the key on disk is not the one every process uses")
	}
}

// useAuthDir points the store and key at a temp dir with the file backend.
func useAuthDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CHATCLI_AUTH_DIR", dir)
	t.Setenv("CHATCLI_KEYCHAIN_BACKEND", "file")
	InvalidateCache()
	t.Cleanup(InvalidateCache)
	return dir
}

// A store that does not decrypt is read-only: the empty in-memory store
// must never be written over the credentials still in the file.
func TestStore_DecryptFailureIsReadOnly(t *testing.T) {
	dir := useAuthDir(t)
	if err := UpsertProfile("anthropic:default", &AuthProfileCredential{Provider: "anthropic", Key: "fake-credential-for-tests"}, nil); err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(dir, "auth-profiles.json")
	before, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}

	// The key that encrypted the store is gone (another key took its place).
	if err := os.WriteFile(filepath.Join(dir, keyFileName), bytes.Repeat([]byte{0x7e}, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	InvalidateCache()

	if got := LoadStore(nil); len(got.Profiles) != 0 {
		t.Fatalf("an undecryptable store produced profiles: %d", len(got.Profiles))
	}
	err = UpsertProfile("openai:default", &AuthProfileCredential{Provider: "openai", Key: "fake-other"}, nil)
	if err == nil {
		t.Fatal("UpsertProfile wrote over an undecryptable store")
	}
	if !strings.Contains(err.Error(), keychainEnvVar) {
		t.Errorf("the error does not explain how to recover: %v", err)
	}
	if err := DeleteProfile("anthropic:default", nil); err == nil {
		t.Fatal("DeleteProfile wrote over an undecryptable store")
	}
	if err := SaveStore(NewAuthProfileStore(), nil); err == nil {
		t.Fatal("SaveStore wrote over an undecryptable store")
	}
	// A save straight after start-up (no load yet) still checks the file.
	InvalidateCache()
	if err := SaveStore(NewAuthProfileStore(), nil); err == nil {
		t.Fatal("SaveStore replaced a store it never managed to open")
	}
	after, _ := os.ReadFile(storePath)
	if !bytes.Equal(before, after) {
		t.Fatal("the credential store file changed after a decrypt failure")
	}
}

// Every replacement of the store keeps the previous version beside it.
func TestStore_BackupBeforeReplacing(t *testing.T) {
	dir := useAuthDir(t)
	if err := UpsertProfile("a", &AuthProfileCredential{Provider: "anthropic", Key: "fake-one"}, nil); err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(dir, "auth-profiles.json")
	first, _ := os.ReadFile(storePath)
	if err := UpsertProfile("b", &AuthProfileCredential{Provider: "openai", Key: "fake-two"}, nil); err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(storePath + storeBackupSuffix)
	if err != nil {
		t.Fatalf("no backup was kept: %v", err)
	}
	if !bytes.Equal(backup, first) {
		t.Fatal("the backup is not the previous store")
	}
	for _, p := range []string{storePath, storePath + storeBackupSuffix} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %v, want 0600", p, info.Mode().Perm())
		}
	}
}

// The master key must never appear on the security command line, where
// every local user can read it from the process table.
func TestMacSetCommand_KeepsTheSecretOutOfArgv(t *testing.T) {
	secret := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32))
	cmd, err := macSetCommand(keychainAccount, []byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range cmd.Args {
		if strings.Contains(a, secret) {
			t.Fatalf("the secret is in argv: %q", cmd.Args)
		}
	}
	if len(cmd.Args) != 2 || cmd.Args[1] != "-i" {
		t.Fatalf("argv = %q, want the interactive mode only", cmd.Args)
	}
	stdin, err := io.ReadAll(cmd.Stdin)
	if err != nil {
		t.Fatal(err)
	}
	line := string(stdin)
	if !strings.Contains(line, " -w "+secret+" ") || !strings.HasSuffix(line, "\n") || strings.Count(line, "\n") != 1 {
		t.Fatalf("stdin command = %q", line)
	}
	if strings.Contains(line, "delete-generic-password") {
		t.Fatal("the item must be updated in place, never deleted first")
	}
	// Anything the interactive parser would split or reinterpret is refused.
	for _, bad := range []string{"a b", "x\nadd-generic-password", `q"uote`, "back\\slash", "single'q", ""} {
		if _, err := macSetCommand(keychainAccount, []byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

// The fallback for filesystems without hard links keeps the exclusive,
// never-overwrite contract.
func TestCreateKeyFileExclusiveFallback(t *testing.T) {
	path := keyPathIn(t)
	first, err := createKeyFileExclusive(path, bytes.Repeat([]byte{0x01}, 32))
	if err != nil {
		t.Fatal(err)
	}
	second, err := createKeyFileExclusive(path, bytes.Repeat([]byte{0x02}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("the fallback overwrote or ignored the existing key")
	}
}

// No temp file is left behind after publishing the key.
func TestCreateKeyFileLeavesNoTempFiles(t *testing.T) {
	path := keyPathIn(t)
	if _, err := createKeyFile(path, bytes.Repeat([]byte{0x03}, 32)); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".auth-key-") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}
