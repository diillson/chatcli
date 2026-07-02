/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package ctxmgr

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

// TestLoadAllContextsQuarantinesCorruptFile pins the recovery contract for
// knowledge bases: a torn/corrupt context JSON (multi-MB — it embeds the whole
// corpus) must be QUARANTINED with a visible name, not silently skipped on
// every start. Silently skipping is how a knowledge base "disappears" from
// /context list with no trace, and the corrupt bytes stay in place, reparsed
// and re-warned about forever.
func TestLoadAllContextsQuarantinesCorruptFile(t *testing.T) {
	dir := t.TempDir()
	s := &Storage{basePath: dir, logger: zap.NewNop()}

	good := &FileContext{ID: "good-ctx", Name: "good", Mode: ModeFull, CreatedAt: time.Now()}
	if err := s.SaveContext(good); err != nil {
		t.Fatal(err)
	}
	corruptPath := filepath.Join(dir, "broken-ctx.json")
	if err := os.WriteFile(corruptPath, []byte(`{"id":"broken-ctx","files":[{"content":"trunc`), 0o600); err != nil {
		t.Fatal(err)
	}

	contexts, err := s.LoadAllContexts()
	if err != nil {
		t.Fatalf("LoadAllContexts: %v", err)
	}
	if len(contexts) != 1 || contexts[0].ID != "good-ctx" {
		t.Fatalf("expected only the good context, got %d", len(contexts))
	}

	// The corrupt file must be moved aside for inspection…
	if _, err := os.Stat(corruptPath); !os.IsNotExist(err) {
		t.Fatal("corrupt context left in place — reparsed and re-warned about on every start")
	}
	entries, _ := os.ReadDir(dir)
	found := false
	for _, e := range entries {
		if strings.Contains(e.Name(), "broken-ctx.json.corrupt") {
			found = true
		}
	}
	if !found {
		t.Fatal("corrupt context was not quarantined with a recoverable name")
	}
}

// TestSaveContextIsAtomic pins the write mechanics: content lands intact on
// overwrite and no temp litter remains.
func TestSaveContextIsAtomic(t *testing.T) {
	dir := t.TempDir()
	s := &Storage{basePath: dir, logger: zap.NewNop()}

	fc := &FileContext{ID: "ctx-a", Name: "first", Mode: ModeFull, CreatedAt: time.Now()}
	if err := s.SaveContext(fc); err != nil {
		t.Fatal(err)
	}
	fc.Name = "second"
	if err := s.SaveContext(fc); err != nil {
		t.Fatal(err)
	}

	loaded, err := s.LoadContext("ctx-a")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Name != "second" {
		t.Fatalf("Name = %q after overwrite, want %q", loaded.Name, "second")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Fatalf("temp litter left behind: %s", e.Name())
		}
	}
}
