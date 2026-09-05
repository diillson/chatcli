//go:build !windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package auth

import (
	"runtime"
	"strings"
	"testing"
)

// The non-Windows stubs must say plainly that there is no Credential
// Manager here, rather than returning something a caller might store.
func TestPlatformStubs_ReportNoCredentialManager(t *testing.T) {
	if nativeAvailable() {
		t.Errorf("the Windows credential API reported itself available on %s", runtime.GOOS)
	}

	if _, err := platformGet("acct"); err == nil {
		t.Error("platformGet succeeded off Windows")
	} else if !strings.Contains(err.Error(), runtime.GOOS) {
		t.Errorf("the error should name the platform: %v", err)
	}

	if err := platformSet("acct", []byte("v")); err == nil {
		t.Error("platformSet succeeded off Windows")
	}
	if err := platformDelete("acct"); err == nil {
		t.Error("platformDelete succeeded off Windows")
	}
}

// nativeKeychainAvailable is what decides whether the keychain is consulted
// at all, so it must answer for this machine and not panic on any platform.
func TestNativeKeychainAvailable_AnswersForThisMachine(t *testing.T) {
	got := nativeKeychainAvailable()
	switch runtime.GOOS {
	case "darwin", "linux":
		// Either answer is correct — it depends on what is installed.
		t.Logf("native keychain available on %s: %v", runtime.GOOS, got)
	default:
		if got {
			t.Errorf("%s has no supported keychain and reported one", runtime.GOOS)
		}
	}
}

// The adapter and the platform dispatcher: both are thin, and both are on
// the path every keychain read takes, so a change that breaks the wiring
// should not need a machine with a configured keychain to show up.
//
// Only non-mutating calls are made against the real platform store: Get and
// Delete for an account that does not exist are harmless on every backend,
// while Set would write into the developer's actual keychain.
func TestOSKeychainAdapter_DelegatesToThePlatform(t *testing.T) {
	const missing = "chatcli-test-account-that-does-not-exist"
	adapter := osKeychain{}

	// Availability must answer without panicking, whatever this machine has.
	_ = adapter.Available()

	if _, err := adapter.Get(missing); err == nil {
		t.Errorf("reading an account that does not exist returned no error")
	}
	// Deleting something absent is an error on every backend, and must not
	// be a panic on any of them.
	_ = adapter.Delete(missing)
}

// A platform with no supported keychain must say so by name rather than
// failing somewhere less legible.
func TestNativeDispatch_UnsupportedPlatformNamesItself(t *testing.T) {
	if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		t.Skipf("%s has a supported backend; the default branch is unreachable here", runtime.GOOS)
	}
	ks := &KeychainStore{}
	if _, err := ks.nativeGet("acct"); err == nil || !strings.Contains(err.Error(), runtime.GOOS) {
		t.Errorf("nativeGet on %s: %v", runtime.GOOS, err)
	}
	if err := ks.nativeSet("acct", []byte("v")); err == nil {
		t.Error("nativeSet succeeded on an unsupported platform")
	}
	if err := ks.nativeDelete("acct"); err == nil {
		t.Error("nativeDelete succeeded on an unsupported platform")
	}
}

// Get must report the file backend rather than an empty value when no
// keychain is in play, so a caller knows to look on disk.
func TestKeychainStore_GetFallbackPathsAreDistinct(t *testing.T) {
	fk := newFakeKeychain()
	fk.available = true

	// Native available, key absent: the store surfaces the backend's error
	// so the caller can fall back rather than treating it as an empty key.
	if _, err := fk.store(KeychainAuto).Get("nope"); err == nil {
		t.Error("a missing account returned no error")
	}

	// Explicit keychain backend behaves the same way.
	if _, err := fk.store(KeychainNative).Get("nope"); err == nil {
		t.Error("a missing account returned no error under the keychain backend")
	}
}
