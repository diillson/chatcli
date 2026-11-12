/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package hooks

import (
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
)

// A reload must reflect the files as they are now: a hook removed from the
// configuration is gone, and nothing is loaded twice.
func TestReloadReflectsTheFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CHATCLI_HOME", "")
	t.Chdir(t.TempDir())

	m := NewManager(zap.NewNop())
	m.LoadFromSettings()
	base := len(m.GetHooks())

	// Whatever directory the manager reads its global file from, a hook
	// planted in memory must not survive a reload that finds no such file.
	m.mu.Lock()
	m.hooks = append(m.hooks, HookConfig{Name: "stale"})
	m.mu.Unlock()
	m.Reload()
	for _, h := range m.GetHooks() {
		if h.Name == "stale" {
			t.Fatal("a hook no file declares survived the reload")
		}
	}
	if got := len(m.GetHooks()); got != base {
		t.Fatalf("reload changed the hook count from %d to %d with the files unchanged", base, got)
	}
	// Twice in a row is the same as once.
	m.Reload()
	if got := len(m.GetHooks()); got != base {
		t.Fatalf("a second reload left %d hooks, want %d", got, base)
	}
	_ = os.Remove(filepath.Join(home, "none"))
}
