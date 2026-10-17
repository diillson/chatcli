/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package atrest

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestRecoveryFile_RoundTripAndWrongPassphrase(t *testing.T) {
	key := "0f0e0d0c0b0a09080706050403020100f0e0d0c0b0a090807060504030201000"
	data, err := SealRecoveryKey(key, "correct horse battery", "abcd1234")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), key) {
		t.Fatal("the recovery file holds the key in clear")
	}
	got, err := OpenRecoveryKey(data, "correct horse battery")
	if err != nil || got != key {
		t.Fatalf("OpenRecoveryKey = %q, %v", got, err)
	}
	if _, err := OpenRecoveryKey(data, "wrong horse battery"); !errors.Is(err, ErrRecoveryPassphrase) {
		t.Fatalf("a wrong passphrase opened the file: %v", err)
	}
	other, _ := SealRecoveryKey(key, "correct horse battery", "abcd1234")
	if string(other) == string(data) {
		t.Fatal("two recovery files are identical: salt or nonce reused")
	}
}

func TestRecoveryFile_RejectsShortPassphraseAndTampering(t *testing.T) {
	if _, err := SealRecoveryKey("k", "short", ""); err == nil {
		t.Fatal("a short passphrase was accepted")
	}
	data, err := SealRecoveryKey("some-recovery-key-value", "a long enough passphrase", "fp01")
	if err != nil {
		t.Fatal(err)
	}
	var f map[string]any
	_ = json.Unmarshal(data, &f)
	f["fingerprint"] = "fp02"
	tampered, _ := json.Marshal(f)
	if _, err := OpenRecoveryKey(tampered, "a long enough passphrase"); !errors.Is(err, ErrRecoveryPassphrase) {
		t.Fatalf("a modified file opened: %v", err)
	}
	f["fingerprint"] = "fp01"
	f["memory_kib"] = 64 * 1024 * 1024
	huge, _ := json.Marshal(f)
	if _, err := OpenRecoveryKey(huge, "a long enough passphrase"); err == nil || !strings.Contains(err.Error(), "out of range") {
		t.Fatalf("an oversized KDF parameter was not refused: %v", err)
	}
	if _, err := OpenRecoveryKey([]byte("not json"), "x"); err == nil {
		t.Fatal("garbage parsed as a recovery file")
	}
}
