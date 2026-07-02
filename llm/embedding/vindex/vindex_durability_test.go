/*
 * ChatCLI - vindex durability tests: corrupt-cache discard and atomic persist.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package vindex

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestLoadDiscardsCorruptCache pins the recovery behavior: a torn/corrupt
// vector cache has no recovery value (vectors are re-embeddable), so load
// must discard it — never leave it in place to be re-warned about on every
// start.
func TestLoadDiscardsCorruptCache(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vector_index.json")
	if err := os.WriteFile(path, []byte(`{"dimension":8,"entries":{"a":[0.1,`), 0o600); err != nil {
		t.Fatal(err)
	}

	idx := New(path, &oneHotProvider{name: "onehot", dim: 8})
	if idx.Count() != 0 {
		t.Fatalf("corrupt cache loaded %d entries, want 0", idx.Count())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("corrupt cache file left in place — it would be re-parsed and re-warned about on every start")
	}

	// The index stays fully usable: a fresh Upsert repopulates cleanly.
	if err := idx.Upsert(context.Background(), map[string]string{"a": "alpha text"}); err != nil {
		t.Fatalf("Upsert after discard: %v", err)
	}
	if idx.Count() != 1 {
		t.Fatalf("Count = %d after re-upsert, want 1", idx.Count())
	}
}

// TestPersistLeavesNoTempLitter pins the atomic-write mechanics: after a
// successful persist the directory holds only the index file — a leftover
// .tmp would mean the rename path is broken.
func TestPersistLeavesNoTempLitter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vector_index.json")
	idx := New(path, &oneHotProvider{name: "onehot", dim: 8})

	if err := idx.Upsert(context.Background(), map[string]string{"a": "alpha", "b": "beta"}); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Fatalf("temp litter left behind: %s", e.Name())
		}
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("index file missing after persist: %v", err)
	}
}

// TestPersistSurfacesUnwritableDir pins the error path: when the store
// directory cannot receive the temp file, Upsert must surface the persist
// failure instead of swallowing it.
func TestPersistSurfacesUnwritableDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory write permissions are advisory on Windows")
	}
	dir := t.TempDir()
	sub := filepath.Join(dir, "store")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatal(err)
	}
	idx := New(filepath.Join(sub, "vector_index.json"), &oneHotProvider{name: "onehot", dim: 8})

	if err := os.Chmod(sub, 0o500); err != nil { // no write permission
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o750) })

	if err := idx.Upsert(context.Background(), map[string]string{"a": "alpha"}); err == nil {
		t.Fatal("Upsert on unwritable dir returned nil; persist failure must surface")
	}
}
