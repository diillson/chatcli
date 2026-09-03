/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package ctxmgr

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

func newRefreshManager(t *testing.T) (*Manager, string) {
	t.Helper()
	m, err := NewManagerWithBasePath(t.TempDir(), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(src, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("a.md", "# alpha\nfirst document body\n")
	write("b.md", "# beta\nsecond document body\n")
	return m, src
}

func TestRefreshContext_DiffsStampsAndRebuildsOnlyOnChange(t *testing.T) {
	m, src := newRefreshManager(t)
	ctx := context.Background()
	fc, err := m.CreateContext(ctx, "docs", "", []string{src}, ModeFull, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(fc.SourcePaths) != 1 || len(fc.FileStamps) != 2 {
		t.Fatalf("create must record sources and stamps: %v %v", fc.SourcePaths, fc.FileStamps)
	}
	updatedAt := fc.UpdatedAt

	// Nothing changed: no rebuild, UpdatedAt untouched.
	_, rep, err := m.RefreshContext(ctx, "docs")
	if err != nil || rep.Dirty() || rep.Unchanged != 2 {
		t.Fatalf("unchanged refresh: rep=%s err=%v", rep, err)
	}
	if !fc.UpdatedAt.Equal(updatedAt) {
		t.Fatal("an unchanged refresh must not bump UpdatedAt (retrieval caches stay valid)")
	}

	// Edit one file, add one, remove one.
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(src, "a.md"), []byte("# alpha\nEDITED body\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "c.md"), []byte("# gamma\nthird\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(src, "b.md")); err != nil {
		t.Fatal(err)
	}
	fc2, rep, err := m.RefreshContext(ctx, "docs")
	if err != nil || rep.Changed != 1 || rep.Added != 1 || rep.Removed != 1 {
		t.Fatalf("dirty refresh: rep=%s err=%v", rep, err)
	}
	if fc2.FileCount != 2 || !fc2.UpdatedAt.After(updatedAt) {
		t.Fatalf("rebuild must replace files and bump UpdatedAt: count=%d", fc2.FileCount)
	}
	found := false
	for _, f := range fc2.Files {
		if filepath.Base(f.Path) == "a.md" && f.Content == "# alpha\nEDITED body\n" {
			found = true
		}
	}
	if !found {
		t.Fatal("edited content must be re-read")
	}

	// A touch (same content, new mtime) is not a change.
	now := time.Now().Add(time.Second)
	if err := os.Chtimes(filepath.Join(src, "c.md"), now, now); err != nil {
		t.Fatal(err)
	}
	_, rep, err = m.RefreshContext(ctx, "docs")
	if err != nil || rep.Dirty() {
		t.Fatalf("a touch must not count as a change: rep=%s err=%v", rep, err)
	}
}

func TestRefreshContext_LegacyContextWithoutSources(t *testing.T) {
	m, _ := newRefreshManager(t)
	m.contexts["legacy"] = &FileContext{ID: "legacy", Name: "old", Mode: ModeFull}
	_, _, err := m.RefreshContext(context.Background(), "old")
	if !errors.Is(err, ErrNoSourcePaths) {
		t.Fatalf("legacy contexts must report ErrNoSourcePaths, got %v", err)
	}
	if _, _, err := m.RefreshContext(context.Background(), "nope"); err == nil {
		t.Fatal("unknown context must error")
	}
}

func TestContextWatcher_RefreshesOnChangeAndNotifies(t *testing.T) {
	m, src := newRefreshManager(t)
	ctx := context.Background()
	if _, err := m.CreateContext(ctx, "live", "", []string{src}, ModeFull, nil, false); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var got []RefreshReport
	w := NewContextWatcher(m, zap.NewNop(), func(name string, rep RefreshReport, err error) {
		mu.Lock()
		defer mu.Unlock()
		if err == nil && name == "live" {
			got = append(got, rep)
		}
	})
	defer w.Close()
	if err := w.Watch("live"); err != nil {
		t.Fatal(err)
	}
	if err := w.Watch("live"); err != nil {
		t.Fatal("watch must be idempotent")
	}
	if names := w.Watching(); len(names) != 1 || names[0] != "live" {
		t.Fatalf("watching = %v", names)
	}
	if err := os.WriteFile(filepath.Join(src, "a.md"), []byte("# alpha\nchanged by the editor\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("watcher must refresh after a change")
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	first := got[0]
	mu.Unlock()
	if first.Changed != 1 {
		t.Fatalf("expected one changed file, got %s", first)
	}
	if !w.Unwatch("live") || w.Unwatch("live") {
		t.Fatal("unwatch reports whether the context was watched")
	}
	if err := w.Watch("missing"); !errors.Is(err, ErrContextNotFound) {
		t.Fatalf("unknown context: %v", err)
	}
}

func TestWatchDirsFor_SkipsNoiseDirs(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"src", "src/pkg", "node_modules", ".git"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(root, "src", "f.go")
	if err := os.WriteFile(file, []byte("package x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dirs := watchDirsFor([]string{root, file})
	joined := map[string]bool{}
	for _, d := range dirs {
		joined[filepath.Base(d)] = true
	}
	if !joined["src"] || !joined["pkg"] || joined["node_modules"] || joined[".git"] {
		t.Fatalf("dirs = %v", dirs)
	}
}
