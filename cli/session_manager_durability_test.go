/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// TestSaveSessionV2_AtomicNoTempLeftover pins the durability contract: the
// session lands whole, readable back, and no temp file survives the rename.
// The store is shared by the REPL, the gateway daemon and the MCP server,
// so a torn write would corrupt cross-process continuity.
func TestSaveSessionV2_AtomicNoTempLeftover(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sm, err := NewSessionManager(zap.NewNop())
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	sd := &SessionData{ChatHistory: []models.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "world"},
	}}
	if err := sm.SaveSessionV2("atomic-test", sd); err != nil {
		t.Fatalf("SaveSessionV2: %v", err)
	}

	loaded, err := sm.LoadSessionV2("atomic-test")
	if err != nil {
		t.Fatalf("LoadSessionV2: %v", err)
	}
	if len(loaded.ChatHistory) != 2 || loaded.ChatHistory[1].Content != "world" {
		t.Fatalf("roundtrip mismatch: %+v", loaded.ChatHistory)
	}

	entries, err := os.ReadDir(sm.sessionsDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("temp file leaked: %s", filepath.Join(sm.sessionsDir, e.Name()))
		}
	}

	// Overwrite must also be atomic and win whole-file.
	sd.ChatHistory = append(sd.ChatHistory, models.Message{Role: "user", Content: "again"})
	if err := sm.SaveSessionV2("atomic-test", sd); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	loaded, err = sm.LoadSessionV2("atomic-test")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.ChatHistory) != 3 {
		t.Fatalf("overwrite lost data: %+v", loaded.ChatHistory)
	}
}
