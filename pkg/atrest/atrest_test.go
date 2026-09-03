/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package atrest

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
)

func TestSealOpen_RoundTripWhenEnabled(t *testing.T) {
	t.Setenv(EnvKey, "unit-test-secret")
	plain := []byte(`{"version":2,"chat_history":[{"role":"user","content":"hi"}]}`)

	sealed, err := Seal(plain)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if !IsEncrypted(sealed) {
		t.Fatal("sealed payload must carry the magic header")
	}
	if bytes.Contains(sealed, []byte("chat_history")) {
		t.Fatal("ciphertext leaks plaintext")
	}
	opened, err := Open(sealed)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(opened, plain) {
		t.Fatalf("round trip mismatch: %q", opened)
	}
}

func TestSealOpen_PassthroughWhenDisabled(t *testing.T) {
	t.Setenv(EnvKey, "")
	plain := []byte("plaintext store")
	sealed, err := Seal(plain)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if !bytes.Equal(sealed, plain) {
		t.Fatal("Seal must be a no-op without a key")
	}
	opened, err := Open(plain)
	if err != nil || !bytes.Equal(opened, plain) {
		t.Fatalf("Open must pass plaintext through, got %q err=%v", opened, err)
	}
}

func TestOpen_PlaintextPassesThroughEvenWithKey(t *testing.T) {
	// Transparent migration: a store written before the key existed keeps
	// loading once the key is configured.
	t.Setenv(EnvKey, "unit-test-secret")
	plain := []byte(`{"version":2}`)
	opened, err := Open(plain)
	if err != nil || !bytes.Equal(opened, plain) {
		t.Fatalf("plaintext must pass through with a key set, got %q err=%v", opened, err)
	}
}

func TestOpen_EncryptedWithoutKeyIsExplicitError(t *testing.T) {
	t.Setenv(EnvKey, "unit-test-secret")
	sealed, err := Seal([]byte("secret session"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	t.Setenv(EnvKey, "")
	if _, err := Open(sealed); !errors.Is(err, ErrKeyMissing) {
		t.Fatalf("expected ErrKeyMissing, got %v", err)
	}
}

func TestOpen_WrongKeyOrTamperFails(t *testing.T) {
	t.Setenv(EnvKey, "key-one")
	sealed, err := Seal([]byte("secret session"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	t.Setenv(EnvKey, "key-two")
	if _, err := Open(sealed); err == nil || !strings.Contains(err.Error(), "decryption failed") {
		t.Fatalf("wrong key must fail authentication, got %v", err)
	}

	t.Setenv(EnvKey, "key-one")
	tampered := append([]byte(nil), sealed...)
	tampered[len(tampered)-1] ^= 0xFF
	if _, err := Open(tampered); err == nil {
		t.Fatal("tampered payload must fail authentication")
	}
}

func TestDerivation_MatchesDocumentedScheme(t *testing.T) {
	// Two encryptors built from the same master and info must open each
	// other's payloads: that is what lets cli.SessionEncryptor and the
	// env-driven Seal share files.
	master := sha256.Sum256([]byte("shared"))
	a, err := NewFromMaster(master[:], SessionInfo)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewFromMaster(master[:], SessionInfo)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := a.Encrypt([]byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if out, err := b.Decrypt(sealed); err != nil || string(out) != "x" {
		t.Fatalf("cross-open failed: %q %v", out, err)
	}
	if _, err := NewFromMaster(nil, SessionInfo); err == nil {
		t.Fatal("empty master must be rejected")
	}
	if _, err := a.Decrypt([]byte("not encrypted")); err == nil {
		t.Fatal("Decrypt must reject payloads without the header")
	}
}
