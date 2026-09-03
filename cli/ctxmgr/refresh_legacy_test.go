/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package ctxmgr

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

func TestInferSourcePaths_FromExistingAbsoluteFiles(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.go")
	if err := os.WriteFile(a, []byte("package a"), 0o600); err != nil {
		t.Fatal(err)
	}
	fc := &FileContext{Files: []utils.FileInfo{
		{Path: a}, {Path: a + "#chunk-2"}, {Path: "relative.go"}, {Path: filepath.Join(dir, "gone.go")}, {Path: dir},
	}}
	got := inferSourcePaths(fc)
	if len(got) != 1 || got[0] != a {
		t.Fatalf("inferred = %v", got)
	}
	if inferSourcePaths(nil) != nil || len(inferSourcePaths(&FileContext{})) != 0 {
		t.Fatal("nothing to infer")
	}
}

func TestWatcher_AddsNestedDirsCreatedAtOnce(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	w := NewContextWatcher(nil, zap.NewNop(), nil)
	if err := w.ensureStarted(); err != nil {
		t.Skip("fsnotify unavailable:", err)
	}
	t.Cleanup(w.Close)
	w.mu.Lock()
	_ = w.watcher.Add(root)
	w.byDir[root] = "ctx"
	w.names["ctx"] = []string{root}
	w.mu.Unlock()
	nested := filepath.Join(root, "pkg", "sub", "deep")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	w.schedule(filepath.Join(root, "pkg")) // what the fsnotify loop would call for the Create event
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, d := range []string{filepath.Join(root, "pkg"), filepath.Join(root, "pkg", "sub"), nested} {
		if w.byDir[d] != "ctx" {
			t.Fatalf("nested dir %s must be watched: %v", d, w.byDir)
		}
	}
	if t0 := w.timers["ctx"]; t0 == nil {
		t.Fatal("a refresh must be scheduled")
	}
}
