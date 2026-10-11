/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKeygenSignVerifyRoundTrip(t *testing.T) {
	var seedOut, pubOut bytes.Buffer
	if code := run([]string{"keygen"}, os.Getenv, &seedOut, &pubOut); code != 0 {
		t.Fatalf("keygen exit %d", code)
	}
	seedB64 := strings.TrimSpace(seedOut.String())
	pubB64 := strings.TrimSpace(pubOut.String())
	seed, err := base64.StdEncoding.DecodeString(seedB64)
	if err != nil || len(seed) != ed25519.SeedSize {
		t.Fatalf("stdout must carry only the base64 seed, got %q", seedB64)
	}
	if strings.Contains(pubOut.String(), seedB64) {
		t.Fatal("the seed must never reach stderr")
	}
	wantPub := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	if pubB64 != base64.StdEncoding.EncodeToString(wantPub) {
		t.Fatal("stderr must carry the public key of the printed seed")
	}

	dir := t.TempDir()
	file := filepath.Join(dir, "checksums.txt")
	content := []byte("abc  chatcli-linux-amd64\n")
	if err := os.WriteFile(file, content, 0o600); err != nil {
		t.Fatal(err)
	}
	env := func(k string) string {
		if k == seedEnv {
			return seedB64
		}
		return ""
	}
	var errOut bytes.Buffer
	if code := run([]string{"sign", file}, env, &bytes.Buffer{}, &errOut); code != 0 {
		t.Fatalf("sign exit %d: %s", code, errOut.String())
	}
	sigFile, err := os.ReadFile(file + ".sig") // #nosec G304 -- test temp path
	if err != nil {
		t.Fatal(err)
	}
	sig, err := base64.StdEncoding.DecodeString(string(sigFile))
	if err != nil || len(sig) != ed25519.SignatureSize {
		t.Fatalf(".sig must be the bare standard base64 of 64 bytes, got %q", sigFile)
	}
	if !ed25519.Verify(wantPub, content, sig) {
		t.Fatal("signature must cover the exact bytes of the file")
	}

	if code := run([]string{"verify", file, file + ".sig", pubB64}, env, &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
		t.Fatal("verify must accept a fresh signature")
	}
	if err := os.WriteFile(file, append(content, 'x'), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"verify", file, file + ".sig", pubB64}, env, &bytes.Buffer{}, &bytes.Buffer{}); code == 0 {
		t.Fatal("verify must reject a modified file")
	}
}

func TestSignRequiresValidSeed(t *testing.T) {
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, seed := range []string{"", "not-base64!", base64.StdEncoding.EncodeToString([]byte("short"))} {
		env := func(string) string { return seed }
		var errOut bytes.Buffer
		if code := run([]string{"sign", file}, env, &bytes.Buffer{}, &errOut); code == 0 {
			t.Fatalf("seed %q must be refused", seed)
		}
		if !strings.Contains(errOut.String(), seedEnv) {
			t.Fatalf("error must name %s, got %q", seedEnv, errOut.String())
		}
	}
	if _, err := os.Stat(file + ".sig"); err == nil {
		t.Fatal("no .sig may be written without a valid seed")
	}
}

func TestUsageErrors(t *testing.T) {
	for _, args := range [][]string{nil, {"bogus"}, {"sign"}, {"verify", "a"}, {"keygen", "x"}} {
		if code := run(args, os.Getenv, &bytes.Buffer{}, &bytes.Buffer{}); code != 2 {
			t.Fatalf("args %v: want exit 2, got %d", args, code)
		}
	}
}
