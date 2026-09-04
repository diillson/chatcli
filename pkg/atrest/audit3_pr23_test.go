/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package atrest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSealAt_BindsThePayloadToItsStore(t *testing.T) {
	t.Setenv(EnvKey, "0123456789abcdef0123456789abcdef-long-random-key")
	a := "/home/u/.chatcli/tenants/alice-aaaa/sessions/work.json"
	b := "/home/u/.chatcli/tenants/bob-bbbb/sessions/work.json"
	sealed, err := SealAt(a, []byte(`{"secret":"alice"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !isV2(sealed) || !IsEncrypted(sealed) {
		t.Fatal("v2 header expected")
	}
	if plain, err := OpenAt(a, sealed); err != nil || string(plain) != `{"secret":"alice"}` {
		t.Fatalf("own store must open: %v", err)
	}
	if _, err := OpenAt(b, sealed); err == nil {
		t.Fatal("a file copied into another tenant's directory must not open")
	}
	if _, err := OpenAt(strings.Replace(a, "work.json", "other.json", 1), sealed); err == nil {
		t.Fatal("a renamed store must not open")
	}
	// The unbound form (no path) round-trips and is v2 too.
	unbound, _ := Seal([]byte("x"))
	if plain, err := Open(unbound); err != nil || string(plain) != "x" {
		t.Fatalf("unbound v2: %v", err)
	}
}

func TestOpenAt_ReadsVersionOneAndRetiredKeys(t *testing.T) {
	t.Setenv(EnvKey, "old-secret")
	legacy, _ := legacyEncryptor("old-secret")
	v1, err := legacy.Encrypt([]byte("legacy payload"))
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := OpenAt("/x/.chatcli/sessions/s.json", v1); err != nil || string(plain) != "legacy payload" {
		t.Fatalf("v1 must still open: %v", err)
	}
	path := "/x/.chatcli/memory/facts.json"
	v2, _ := SealAt(path, []byte("rotated"))
	t.Setenv(EnvKey, "new-secret")
	t.Setenv(EnvPreviousKeys, "old-secret")
	if plain, err := OpenAt(path, v2); err != nil || string(plain) != "rotated" {
		t.Fatalf("a retired key must still open v2: %v", err)
	}
	if sealedWithCurrentKeyAt(path, v2) {
		t.Fatal("sealed with the retired key is not current")
	}
}

func TestShortSecret_IsStretchedAndStillRoundTrips(t *testing.T) {
	t.Setenv(EnvKey, "hunter2")
	sealed, err := SealAt("/x/.chatcli/sessions/s.json", []byte("pw"))
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := OpenAt("/x/.chatcli/sessions/s.json", sealed); err != nil || string(plain) != "pw" {
		t.Fatalf("short secret round trip: %v", err)
	}
	// The v2 key of a short secret is not the v1 key of the same secret.
	short, _ := v2Encryptor("hunter2")
	old, _ := legacyEncryptor("hunter2")
	if string(short.key) == string(old.key) {
		t.Fatal("passphrases must be stretched")
	}
	long := strings.Repeat("k", ShortSecretBytes)
	a, _ := v2Encryptor(long)
	b, _ := legacyEncryptor(long)
	if string(a.key) != string(b.key) {
		t.Fatal("a random key of full length keeps the HKDF derivation")
	}
}

func TestResealFile_RewritesV1AsBoundV2(t *testing.T) {
	t.Setenv(EnvKey, "0123456789abcdef0123456789abcdef-long-random-key")
	dir := filepath.Join(t.TempDir(), ".chatcli", "sessions")
	_ = os.MkdirAll(dir, 0o700)
	path := filepath.Join(dir, "s.json")
	legacy, _ := legacyEncryptor(os.Getenv(EnvKey))
	v1, _ := legacy.Encrypt([]byte("data"))
	_ = os.WriteFile(path, v1, 0o600)
	changed, err := ResealFile(path)
	if err != nil || !changed {
		t.Fatalf("v1 must be resealed: %v %v", changed, err)
	}
	raw, _ := os.ReadFile(path)
	if !isV2(raw) {
		t.Fatal("resealed file must be v2")
	}
	if plain, err := OpenAt(path, raw); err != nil || string(plain) != "data" {
		t.Fatalf("resealed opens at its path: %v", err)
	}
	if changed, _ := ResealFile(path); changed {
		t.Fatal("already current: no rewrite")
	}
}
