/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func TestPersistRedact_StrictPolicyOnly(t *testing.T) {
	secret := "token sk-" + strings.Repeat("a", 30)
	t.Setenv("CHATCLI_ENV_REDACT_MODE", "permissive")
	if persistRedact(secret) != secret {
		t.Fatal("the permissive default keeps persisted stores verbatim")
	}
	t.Setenv("CHATCLI_ENV_REDACT_MODE", "strict")
	if got := persistRedact(secret); strings.Contains(got, strings.Repeat("a", 30)) {
		t.Fatalf("strict must mask: %q", got)
	}
	live := []models.Message{{Role: "user", Content: secret}}
	stored := persistRedactMessages(live)
	if live[0].Content != secret || strings.Contains(stored[0].Content, strings.Repeat("a", 30)) {
		t.Fatal("redaction must copy, never touch the live history")
	}
}

func TestSaveSession_StrictPolicyMasksSecretsOnDisk(t *testing.T) {
	t.Setenv("CHATCLI_ENV_REDACT_MODE", "strict")
	t.Setenv("CHATCLI_ENCRYPTION_KEY", "")
	dir := t.TempDir()
	sm, err := NewSessionManagerAt(dir, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	secret := "ghp_" + strings.Repeat("b", 30)
	history := []models.Message{{Role: "user", Content: "my token is " + secret}}
	if err := sm.SaveSession("s", history); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "s.json"))
	if strings.Contains(string(raw), secret) {
		t.Fatal("the session file must not hold the raw token under strict")
	}
	if history[0].Content != "my token is "+secret {
		t.Fatal("the live history stays intact")
	}
}

func TestHistoryManager_SealsLinesAtRest(t *testing.T) {
	t.Setenv("CHATCLI_ENCRYPTION_KEY", "history-test-key")
	t.Setenv("CHATCLI_DISABLE_HISTORY", "false")
	path := filepath.Join(t.TempDir(), ".chatcli_history")
	t.Setenv("HISTORY_FILE", path)
	hm := NewHistoryManager(zap.NewNop())
	if hm.GetHistoryFilePath() != path {
		t.Skipf("history path not overridable here: %s", hm.GetHistoryFilePath())
	}
	if err := hm.AppendAndRotateHistory([]string{"/session load prod", "hello world"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "hello world") || !strings.Contains(string(raw), historySealedPrefix) {
		t.Fatalf("history must be sealed on disk:\n%s", raw)
	}
	got, err := hm.LoadHistory()
	if err != nil || len(got) != 2 || got[1] != "hello world" {
		t.Fatalf("sealed history must load back: %v %v", got, err)
	}
}

func TestRetention_PrunesOldFilesAndLoops(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "old.json")
	fresh := filepath.Join(dir, "fresh.json")
	other := filepath.Join(dir, "old.txt")
	for _, p := range []string{old, fresh, other} {
		_ = os.WriteFile(p, []byte("x"), 0o600)
	}
	past := time.Now().Add(-48 * time.Hour)
	_ = os.Chtimes(old, past, past)
	_ = os.Chtimes(other, past, past)
	if n := pruneFilesOlderThan(dir, time.Now().Add(-24*time.Hour), ".json"); n != 1 {
		t.Fatalf("pruned %d", n)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("fresh file must survive")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("other extensions untouched when an extension is given")
	}
	if n := pruneFilesOlderThan(filepath.Join(dir, "missing"), time.Now(), ""); n != 0 {
		t.Fatal("missing dir is a no-op")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { (&ChatCLI{logger: zap.NewNop()}).runRetentionLoop(ctx, time.Hour); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("retention loop must stop with its context")
	}
}
