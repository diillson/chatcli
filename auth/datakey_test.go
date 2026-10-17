/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package auth

import (
	"bytes"
	"encoding/hex"
	"path/filepath"
	"testing"
)

func TestDataKeySecret_DerivedStableAndSeparate(t *testing.T) {
	t.Setenv("CHATCLI_AUTH_DIR", t.TempDir())
	t.Setenv("CHATCLI_KEYCHAIN_BACKEND", "file")

	a, where, err := DataKeySecret()
	if err != nil {
		t.Fatal(err)
	}
	if where != "file" {
		t.Errorf("where = %q, want file", where)
	}
	if len(a) != 64 {
		t.Fatalf("secret length = %d, want 64 hex chars", len(a))
	}
	b, _, err := DataKeySecret()
	if err != nil || a != b {
		t.Fatal("the data key is not stable across calls")
	}
	root, err := loadOrCreateKey()
	if err != nil {
		t.Fatal(err)
	}
	if a == hex.EncodeToString(root) {
		t.Error("the data key equals the credential key")
	}
}

// With a key file and a different keychain key both present, the one not
// in use is offered as a read-only fallback, derived like the active one.
func TestDataKeyFallbacks_OffersTheKeyNotInUse(t *testing.T) {
	resetAdopt(t)
	t.Setenv("CHATCLI_AUTH_DIR", "")
	path := keyPathIn(t)
	fileKey := writeKey(t, path, 0x21)
	other := bytes.Repeat([]byte{0x22}, 32)
	fk := newFakeKeychain()
	fk.items[keychainAccount] = encodedKey(other)

	got := fallbackDataSecrets(fk.store(KeychainAuto), path)
	want, _ := deriveDataSecret(other)
	if len(got) != 1 || got[0] != want {
		t.Fatalf("fallbacks = %v, want the keychain key's data secret", got)
	}
	active, _ := deriveDataSecret(fileKey)
	for _, s := range got {
		if s == active {
			t.Fatal("the active key was offered as a fallback")
		}
	}
	if fb := fallbackDataSecrets(newFakeKeychain().store(KeychainFile), keyPathIn(t)); len(fb) != 0 {
		t.Fatalf("a single key produced fallbacks: %v", fb)
	}
}

func TestKeyFilePath_FollowsTheAuthDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CHATCLI_AUTH_DIR", dir)
	if got := KeyFilePath(); got != filepath.Join(dir, ".auth-key") {
		t.Fatalf("KeyFilePath = %q", got)
	}
}

func TestDataKeySecret_ReportsTheKeychainOnceTheKeyMoved(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CHATCLI_AUTH_DIR", dir)
	t.Setenv("CHATCLI_KEYCHAIN_BACKEND", "file")
	if fb := DataKeyFallbacks(); len(fb) != 0 {
		t.Fatalf("a single file key produced fallbacks: %v", fb)
	}
	markKeyInKeychain(filepath.Join(dir, ".auth-key"))
	if _, where, err := DataKeySecret(); err != nil || where != "keychain" {
		t.Fatalf("where = %q, %v; want keychain", where, err)
	}
}
