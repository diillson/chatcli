/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

// Package testenv makes a test process hermetic. ChatCLI keeps its state
// under the user's home (~/.chatcli: costs, checkpoints, memory, logs,
// skills, scheduler) and its credential key in the OS keychain, and every
// constructor that reaches those defaults does so through os.UserHomeDir
// and CHATCLI_KEYCHAIN_BACKEND. A test that builds such an object without
// redirecting them writes into the developer's real store — one full run
// of the suite left 724 files under ~/.chatcli — and on macOS makes the
// keychain ask, once per rebuilt test binary, whether it may read the key
// the real chatcli stored.
//
// Isolate points the home directory at a throwaway directory and pins the
// file keychain backend for the rest of the process. Call it from
// TestMain, before any test runs, because several stores latch their path
// on first use.
package testenv

import (
	"os"
	"runtime"
	"testing"
)

// KeychainBackendEnv is the variable auth.NewKeychainStore reads; "file"
// keeps every test away from the OS credential store.
const KeychainBackendEnv = "CHATCLI_KEYCHAIN_BACKEND"

// Isolate redirects the process home directory to a fresh temporary
// directory and selects the file keychain backend. It returns the
// directory and a cleanup that removes it. Environment changes are
// process-wide on purpose: TestMain has no *testing.T to scope them to,
// and the redirect must precede the first test.
func Isolate() (home string, cleanup func()) {
	home, err := os.MkdirTemp("", "chatcli-test-home-*")
	if err != nil {
		// No temp dir means no isolation is possible; the caller's tests
		// still run, against the real home, as they did before.
		return "", func() {}
	}
	_ = os.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		// os.UserHomeDir reads USERPROFILE there, and the per-user app
		// data dirs some stores prefer live beside it.
		_ = os.Setenv("USERPROFILE", home)
		_ = os.Setenv("LOCALAPPDATA", home)
		_ = os.Setenv("APPDATA", home)
	}
	_ = os.Setenv(KeychainBackendEnv, "file")
	return home, func() { _ = os.RemoveAll(home) }
}

// Main is the TestMain body for a package whose tests reach the home
// store or the keychain: isolate, run the package's own setup hooks (i18n
// initialization, theme profile, ...), run the tests, clean up, exit.
func Main(m *testing.M, setup ...func()) {
	_, cleanup := Isolate()
	for _, fn := range setup {
		fn()
	}
	code := m.Run()
	cleanup()
	os.Exit(code)
}
