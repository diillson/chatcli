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
	"github.com/diillson/chatcli/pkg/atrest"
	"go.uber.org/zap"
)

func newAtRestSessionManager(t *testing.T) *SessionManager {
	t.Helper()
	return &SessionManager{sessionsDir: t.TempDir(), logger: zap.NewNop()}
}

func sampleSession() *SessionData {
	return &SessionData{
		Version: 2,
		ChatHistory: []models.Message{
			{Role: "user", Content: "remember the launch codes: alpha-bravo"},
			{Role: "assistant", Content: "noted"},
		},
	}
}

func TestSessionStore_EncryptedRoundTripWhenKeySet(t *testing.T) {
	t.Setenv(atrest.EnvKey, "session-test-key")
	sm := newAtRestSessionManager(t)

	if err := sm.SaveSessionV2("work", sampleSession()); err != nil {
		t.Fatalf("save: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(sm.sessionsDir, "work.json"))
	if err != nil {
		t.Fatalf("read raw: %v", err)
	}
	if !atrest.IsEncrypted(raw) {
		t.Fatal("session file must be encrypted at rest when the key is set")
	}
	if strings.Contains(string(raw), "launch codes") {
		t.Fatal("plaintext leaked into the encrypted session file")
	}

	sd, err := sm.LoadSessionV2("work")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(sd.ChatHistory) != 2 || sd.ChatHistory[0].Content != "remember the launch codes: alpha-bravo" {
		t.Fatalf("round trip mismatch: %+v", sd.ChatHistory)
	}
	// The search corpus goes through the same loader.
	hits, err := sm.SearchSessions("launch codes", 2)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("encrypted sessions must remain searchable through the store")
	}
}

func TestSessionStore_PlaintextMigratesOnNextSave(t *testing.T) {
	t.Setenv(atrest.EnvKey, "")
	sm := newAtRestSessionManager(t)
	if err := sm.SaveSessionV2("legacy", sampleSession()); err != nil {
		t.Fatalf("save plaintext: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(sm.sessionsDir, "legacy.json"))
	if atrest.IsEncrypted(raw) {
		t.Fatal("no key configured: file must stay plaintext (legacy behavior)")
	}

	// Key appears later: the plaintext file still loads…
	t.Setenv(atrest.EnvKey, "session-test-key")
	sd, err := sm.LoadSessionV2("legacy")
	if err != nil {
		t.Fatalf("plaintext session must keep loading once a key is set: %v", err)
	}
	// …and is sealed on its next save.
	if err := sm.SaveSessionV2("legacy", sd); err != nil {
		t.Fatalf("re-save: %v", err)
	}
	raw, _ = os.ReadFile(filepath.Join(sm.sessionsDir, "legacy.json"))
	if !atrest.IsEncrypted(raw) {
		t.Fatal("re-saved session must be encrypted")
	}
}

func TestSessionStore_EncryptedWithoutKeyIsClearError(t *testing.T) {
	t.Setenv(atrest.EnvKey, "session-test-key")
	sm := newAtRestSessionManager(t)
	if err := sm.SaveSessionV2("locked", sampleSession()); err != nil {
		t.Fatalf("save: %v", err)
	}
	t.Setenv(atrest.EnvKey, "")
	_, err := sm.LoadSessionV2("locked")
	if err == nil {
		t.Fatal("loading an encrypted session without the key must fail")
	}
	if !strings.Contains(err.Error(), atrest.EnvKey) {
		t.Fatalf("error must tell the user which variable is missing, got: %v", err)
	}
}
