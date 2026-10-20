/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package auth

import (
	"path/filepath"
	"testing"
)

// The credential store follows the state directory, and CHATCLI_AUTH_DIR
// still moves it on its own — the pair lets a separate state directory
// keep using the credentials already logged in.
func TestCredentialPathsFollowTheStateDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	state := filepath.Join(t.TempDir(), "state")
	t.Setenv("CHATCLI_STATE_DIR", state)
	t.Setenv("CHATCLI_AUTH_DIR", "")
	if got, want := DefaultStorePath(), filepath.Join(state, "auth-profiles.json"); got != want {
		t.Errorf("store: got %q want %q", got, want)
	}
	if got, want := getKeyPath(), filepath.Join(state, keyFileName); got != want {
		t.Errorf("key: got %q want %q", got, want)
	}

	authDir := t.TempDir()
	t.Setenv("CHATCLI_AUTH_DIR", authDir)
	if got, want := DefaultStorePath(), filepath.Join(authDir, "auth-profiles.json"); got != want {
		t.Errorf("CHATCLI_AUTH_DIR must win: got %q want %q", got, want)
	}
}
