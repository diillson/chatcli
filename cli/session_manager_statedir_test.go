/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// Sessions saved with CHATCLI_STATE_DIR set land in that directory and
// nothing is written under ~/.chatcli.
func TestSessionManagerFollowsTheStateDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CHATCLI_ENCRYPTION_KEY", "off")
	state := filepath.Join(t.TempDir(), "state")
	t.Setenv("CHATCLI_STATE_DIR", state)

	sm, err := NewSessionManager(zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if err := sm.SaveSessionV2("bench", &SessionData{
		Version:     2,
		ChatHistory: []models.Message{{Role: "user", Content: "synthetic"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(state, "sessions", "bench.json")); err != nil {
		t.Errorf("session must be under the state dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".chatcli")); !os.IsNotExist(err) {
		t.Errorf("nothing may be written under ~/.chatcli, stat err = %v", err)
	}
}
