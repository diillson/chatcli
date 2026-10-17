/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package atrest

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withProvider(t *testing.T, p KeyProvider) {
	t.Helper()
	SetKeyProvider(p)
	t.Cleanup(func() { SetKeyProvider(nil) })
}

func TestKeySource_ProviderSealsWhenTheVariableIsUnset(t *testing.T) {
	t.Setenv(EnvKey, "")
	calls := 0
	withProvider(t, func() (string, string, error) {
		calls++
		return "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "keychain", nil
	})
	if !Enabled() {
		t.Fatal("a provider secret did not enable encryption")
	}
	path := filepath.Join(t.TempDir(), ".chatcli", "sessions", "s.json")
	sealed, err := SealAt(path, []byte("hello"))
	if err != nil || !IsEncrypted(sealed) {
		t.Fatalf("SealAt = %v, encrypted=%v", err, IsEncrypted(sealed))
	}
	plain, err := OpenAt(path, sealed)
	if err != nil || !bytes.Equal(plain, []byte("hello")) {
		t.Fatalf("OpenAt = %q, %v", plain, err)
	}
	if src, where, _ := KeyStatus(); src != KeySourceProvider || where != "keychain" {
		t.Errorf("KeyStatus = %s/%s", src, where)
	}
	if calls != 1 {
		t.Errorf("provider ran %d times, want once", calls)
	}
}

func TestKeySource_VariableWinsAndOffDisables(t *testing.T) {
	withProvider(t, func() (string, string, error) { return "provider-secret-provider-secret-xx", "file", nil })

	t.Setenv(EnvKey, "env-secret-env-secret-env-secret-xx")
	if src, _, _ := KeyStatus(); src != KeySourceEnv {
		t.Errorf("KeyStatus = %s, want env", src)
	}
	sealed, err := Seal([]byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvKey, "")
	if _, err := Open(sealed); err == nil {
		t.Error("a payload sealed with the variable opened with the provider key")
	}

	for _, off := range []string{"off", "OFF", "none", "disabled", "false", "0"} {
		t.Setenv(EnvKey, off)
		if Enabled() {
			t.Errorf("%q did not turn encryption off", off)
		}
		if out, _ := Seal([]byte("x")); IsEncrypted(out) {
			t.Errorf("%q still sealed", off)
		}
		if src, _, _ := KeyStatus(); src != KeySourceOff {
			t.Errorf("%q: KeyStatus = %s", off, src)
		}
	}
}

func TestKeySource_FailingProviderLeavesPlaintextAndReportsIt(t *testing.T) {
	t.Setenv(EnvKey, "")
	withProvider(t, func() (string, string, error) { return "", "", errors.New("keychain locked") })
	if Enabled() {
		t.Error("a failing provider enabled encryption")
	}
	src, _, err := KeyStatus()
	if src != KeySourceUnavailable || err == nil {
		t.Errorf("KeyStatus = %s, %v", src, err)
	}
}

func TestKeySource_NoProviderNoVariableIsPlaintext(t *testing.T) {
	t.Setenv(EnvKey, "")
	SetKeyProvider(nil)
	if Enabled() {
		t.Error("encryption on with no secret")
	}
	if src, _, _ := KeyStatus(); src != KeySourceNone {
		t.Errorf("KeyStatus = %s, want none", src)
	}
}

func TestSealLines_SealsPlaintextAndKeepsBlankLines(t *testing.T) {
	t.Setenv(EnvKey, "seal-lines-key-seal-lines-key-00000")
	dir := t.TempDir()
	for _, bound := range []bool{true, false} {
		path := filepath.Join(dir, ".chatcli", "transcripts", "t.jsonl")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{\"a\":1}\n\n{\"b\":2}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		n, err := SealLines(path, "enc:", bound)
		if err != nil || n != 2 {
			t.Fatalf("bound=%v: SealLines = %d, %v", bound, n, err)
		}
		raw, _ := os.ReadFile(path)
		lines := strings.Split(string(raw), "\n")
		if len(lines) != 4 || lines[1] != "" || strings.Contains(string(raw), "\"a\"") {
			t.Fatalf("bound=%v: unexpected layout %q", bound, raw)
		}
		at := ""
		if bound {
			at = path
		}
		payload, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(lines[0], "enc:"))
		plain, err := OpenAt(at, payload)
		if err != nil || string(plain) != "{\"a\":1}" {
			t.Fatalf("bound=%v: line does not open: %q %v", bound, plain, err)
		}
		if n, _ := SealLines(path, "enc:", bound); n != 0 {
			t.Fatalf("bound=%v: a second pass rewrote %d lines", bound, n)
		}
	}
}

// Turning encryption off stops sealing but never strands sealed data.
func TestKeySource_OffStillOpensWhatWasSealed(t *testing.T) {
	t.Setenv(EnvKey, "")
	withProvider(t, func() (string, string, error) {
		return "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210", "keychain", nil
	})
	path := filepath.Join(t.TempDir(), ".chatcli", "sessions", "s.json")
	sealed, err := SealAt(path, []byte("kept"))
	if err != nil || !IsEncrypted(sealed) {
		t.Fatal("not sealed")
	}
	t.Setenv(EnvKey, "off")
	plain, err := OpenAt(path, sealed)
	if err != nil || string(plain) != "kept" {
		t.Fatalf("sealed data stranded by off: %v", err)
	}
	if KeyFingerprint() != "" {
		t.Error("a fingerprint was reported while encryption is off")
	}
}

func TestKeySource_MissingKeyNamesTheProviderFailure(t *testing.T) {
	t.Setenv(EnvKey, "seal-first-seal-first-seal-first-00")
	path := filepath.Join(t.TempDir(), ".chatcli", "sessions", "s.json")
	sealed, err := SealAt(path, []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvKey, "")
	withProvider(t, func() (string, string, error) { return "", "", errors.New("keychain locked") })
	_, err = OpenAt(path, sealed)
	if !errors.Is(err, ErrKeyMissing) || !strings.Contains(err.Error(), "keychain locked") {
		t.Fatalf("err = %v", err)
	}
}

// Moving from the default key to one of your own: set the variable and the
// data sealed with the keychain key still opens (and reseals onto the new one).
func TestKeySource_ProviderKeyOpensAfterSwitchingToTheVariable(t *testing.T) {
	t.Setenv(EnvKey, "")
	withProvider(t, func() (string, string, error) {
		return "0011223344556677001122334455667700112233445566770011223344556677", "keychain", nil
	})
	path := filepath.Join(t.TempDir(), ".chatcli", "sessions", "s.json")
	sealed, err := SealAt(path, []byte("before"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvKey, "my-own-key-my-own-key-my-own-key-00")
	plain, err := OpenAt(path, sealed)
	if err != nil || string(plain) != "before" {
		t.Fatalf("data sealed with the keychain key stranded: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, sealed, 0o600); err != nil {
		t.Fatal(err)
	}
	if changed, err := ResealFile(path); err != nil || !changed {
		t.Fatalf("reseal onto the new key: %v %v", changed, err)
	}
	SetKeyProvider(nil)
	raw, _ := os.ReadFile(path)
	if plain, err := OpenAt(path, raw); err != nil || string(plain) != "before" {
		t.Fatalf("resealed file does not open with the new key alone: %v", err)
	}
}

func TestKeySource_FailedProviderIsRetried(t *testing.T) {
	t.Setenv(EnvKey, "")
	calls := 0
	withProvider(t, func() (string, string, error) {
		calls++
		if calls == 1 {
			return "", "", errors.New("keychain locked")
		}
		return "99887766554433229988776655443322998877665544332299887766554433aa", "keychain", nil
	})
	if Enabled() {
		t.Fatal("enabled while the provider failed")
	}
	providerMu.Lock()
	providerAt = providerAt.Add(-2 * providerRetry)
	providerMu.Unlock()
	if !Enabled() {
		t.Fatal("the provider was not asked again after the retry window")
	}
}

func TestKeySource_FallbacksOpenWhatAnotherKeySealed(t *testing.T) {
	t.Setenv(EnvKey, "")
	other := "1111111111111111111111111111111111111111111111111111111111111111"
	withProvider(t, func() (string, string, error) { return other, "file", nil })
	path := filepath.Join(t.TempDir(), ".chatcli", "sessions", "s.json")
	sealed, err := SealAt(path, []byte("from the old key"))
	if err != nil {
		t.Fatal(err)
	}
	SetKeyProvider(func() (string, string, error) {
		return "2222222222222222222222222222222222222222222222222222222222222222", "keychain", nil
	})
	if _, err := OpenAt(path, sealed); err == nil {
		t.Fatal("opened without the fallback")
	}
	SetKeyFallbacks(func() []string { return []string{other} })
	t.Cleanup(func() { SetKeyFallbacks(nil) })
	if plain, err := OpenAt(path, sealed); err != nil || string(plain) != "from the old key" {
		t.Fatalf("fallback did not open it: %v", err)
	}
}

func TestReadAttachment_OpensOnlyAttachments(t *testing.T) {
	t.Setenv(EnvKey, "attach-key-attach-key-attach-key-00")
	root := filepath.Join(t.TempDir(), ".chatcli")
	att := filepath.Join(root, AttachmentsDirName, "a.png")
	ses := filepath.Join(root, "sessions", "s.json")
	for _, p := range []string{att, ses} {
		sealed, err := SealAt(p, []byte("secret content"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, sealed, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := ReadAttachment(att); err != nil || string(got) != "secret content" {
		t.Fatalf("attachment did not open: %v", err)
	}
	if got, _ := ReadAttachment(ses); strings.Contains(string(got), "secret content") {
		t.Fatal("a session store was decrypted for a tool")
	}
}

// The recovery key opens, on a machine without the keychain entry, every
// store the default key sealed.
func TestRecoveryKey_OpensDataWithoutTheProvider(t *testing.T) {
	t.Setenv(EnvKey, "")
	withProvider(t, func() (string, string, error) {
		return "5555555555555555555555555555555555555555555555555555555555555555", "keychain", nil
	})
	path := filepath.Join(t.TempDir(), ".chatcli", "memory", "profile.json")
	sealed, err := SealAt(path, []byte(`{"name":"synthetic"}`))
	if err != nil {
		t.Fatal(err)
	}
	key, err := RecoveryKey()
	if err != nil || key == "" {
		t.Fatalf("RecoveryKey = %q, %v", key, err)
	}
	SetKeyProvider(nil) // the keychain entry is gone
	t.Setenv(EnvKey, key)
	if plain, err := OpenAt(path, sealed); err != nil || string(plain) != `{"name":"synthetic"}` {
		t.Fatalf("the recovery key does not open the data: %v", err)
	}
	if _, err := RecoveryKey(); !errors.Is(err, ErrNoRecoveryKey) {
		t.Fatalf("an env key reported a default recovery key: %v", err)
	}
}
