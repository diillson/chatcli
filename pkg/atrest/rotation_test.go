/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package atrest

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRotation_PreviousKeyOpensAndResealMigrates(t *testing.T) {
	t.Setenv(EnvKey, "old-secret")
	sealedOld, err := Seal([]byte(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "store.json")
	if err := os.WriteFile(file, sealedOld, 0o600); err != nil {
		t.Fatal(err)
	}
	plainFile := filepath.Join(dir, "plain.json")
	_ = os.WriteFile(plainFile, []byte(`{"p":true}`), 0o600)
	lines := filepath.Join(dir, "j.jsonl")
	_ = os.WriteFile(lines, []byte("enc:"+base64.StdEncoding.EncodeToString(sealedOld)+"\n{\"plain\":1}\n"), 0o600)

	// Rotate: new current key, old one retired.
	t.Setenv(EnvKey, "new-secret")
	if _, err := Open(sealedOld); err == nil {
		t.Fatal("without the retired key the old payload must not open")
	}
	t.Setenv(EnvPreviousKeys, " , old-secret ,")
	if plain, err := Open(sealedOld); err != nil || string(plain) != `{"a":1}` {
		t.Fatalf("retired key must still open: %v %q", err, plain)
	}
	if SealedWithCurrentKey(sealedOld) || !SealedWithCurrentKey(mustSeal(t, "x")) || SealedWithCurrentKey([]byte("plain")) {
		t.Fatal("SealedWithCurrentKey semantics")
	}
	fp := KeyFingerprint()
	if len(fp) != 8 {
		t.Fatalf("fingerprint = %q", fp)
	}
	changed, err := ResealFile(file)
	if err != nil || !changed {
		t.Fatalf("reseal: %v %v", err, changed)
	}
	data, _ := os.ReadFile(file)
	if !SealedWithCurrentKey(data) {
		t.Fatal("file must now be sealed with the current key")
	}
	if changed, _ := ResealFile(file); changed {
		t.Fatal("second reseal is a no-op")
	}
	if changed, err := ResealFile(plainFile); err != nil || !changed {
		t.Fatalf("plaintext gets sealed: %v %v", err, changed)
	}
	n, err := ResealLines(lines, "enc:")
	if err != nil || n != 1 {
		t.Fatalf("reseal lines: %v %d", err, n)
	}
	body, _ := os.ReadFile(lines)
	if !strings.Contains(string(body), `{"plain":1}`) || strings.Contains(string(body), base64.StdEncoding.EncodeToString(sealedOld)) {
		t.Fatalf("plain lines kept, sealed lines rewritten: %q", body)
	}
	// Retire the old key: everything still opens.
	t.Setenv(EnvPreviousKeys, "")
	if _, err := Open(mustRead(t, file)); err != nil {
		t.Fatal("resealed store must open with the current key alone")
	}
	t.Setenv(EnvKey, "")
	if _, err := ResealFile(file); err == nil {
		t.Fatal("reseal without a key is an error")
	}
}

func mustSeal(t *testing.T, s string) []byte {
	t.Helper()
	b, err := Seal([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
