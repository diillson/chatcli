//go:build !windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// /export writes the whole conversation: the file must be owner-only under
// the usual 022 umask, also when it replaces a file others could read.
func TestExportCommand_WritesOwnerOnly(t *testing.T) {
	old := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(old) })
	t.Setenv("HOME", t.TempDir())

	c := &ChatCLI{logger: zap.NewNop(), history: []models.Message{{Role: "user", Content: "hello"}, {Role: "assistant", Content: "hi"}}}

	fresh := filepath.Join(t.TempDir(), "fresh.jsonl")
	c.handleExportCommand("/export " + fresh)

	existing := filepath.Join(t.TempDir(), "existing.jsonl")
	if err := os.WriteFile(existing, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	c.handleExportCommand("/export " + existing)

	for _, p := range []string{fresh, existing} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if m := fi.Mode().Perm(); m != 0o600 {
			t.Errorf("%s mode = %v, want 0600", filepath.Base(p), m)
		}
		if fi.Size() == 0 {
			t.Errorf("%s is empty", filepath.Base(p))
		}
	}
}
