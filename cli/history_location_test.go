/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// Typed prompts used to land in ./.chatcli_history, inside project trees,
// where they got committed. The default is now ~/.chatcli/history; a legacy
// per-directory file is still read so nothing is lost, but never written.
func TestHistoryDefaultsToTheChatCLIDirAndKeepsLegacyReadOnly(t *testing.T) {
	home := filepath.Dir(filepath.Dir(withCoderHomeDir(t)))
	t.Setenv("CHATCLI_DISABLE_HISTORY", "")
	t.Setenv("HISTORY_FILE", "")
	_ = os.Unsetenv("HISTORY_FILE")
	project := t.TempDir()
	t.Chdir(project)
	legacy := filepath.Join(project, ".chatcli_history")
	if err := os.WriteFile(legacy, []byte("old prompt\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	hm := NewHistoryManager(zap.NewNop())
	want := filepath.Join(home, ".chatcli", "history")
	if hm.GetHistoryFilePath() != want {
		t.Fatalf("history path = %s, want %s", hm.GetHistoryFilePath(), want)
	}
	if err := hm.AppendAndRotateHistory([]string{"new prompt"}); err != nil {
		t.Fatal(err)
	}
	got, err := hm.LoadHistory()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, "|") != "old prompt|new prompt" {
		t.Fatalf("history = %v", got)
	}
	if data, _ := os.ReadFile(legacy); string(data) != "old prompt\n" {
		t.Fatalf("the legacy file must never be written: %q", data)
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(want); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("history mode: %v %v", fi.Mode().Perm(), err)
		}
	}

	// A symlinked legacy file is not followed.
	if runtime.GOOS != "windows" {
		_ = os.Remove(legacy)
		if err := os.Symlink(filepath.Join(home, "elsewhere"), legacy); err != nil {
			t.Fatal(err)
		}
		if NewHistoryManager(zap.NewNop()).legacyFile != "" {
			t.Fatal("a symlinked legacy history must be ignored")
		}
	}

	// An explicit HISTORY_FILE still wins, with no legacy merge.
	custom := filepath.Join(t.TempDir(), "h")
	t.Setenv("HISTORY_FILE", custom)
	if hm := NewHistoryManager(zap.NewNop()); hm.GetHistoryFilePath() != custom || hm.legacyFile != "" {
		t.Fatalf("explicit path: %s %q", hm.GetHistoryFilePath(), hm.legacyFile)
	}
}
