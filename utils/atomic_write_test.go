/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package utils

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAtomicWriteFile_WritesContentAndPerm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store.json")

	if err := AtomicWriteFile(path, []byte(`{"v":1}`), 0o600); err != nil {
		t.Fatalf("AtomicWriteFile: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != `{"v":1}` {
		t.Fatalf("content = %q, want %q", got, `{"v":1}`)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("perm = %o, want 0600", perm)
		}
	}
}

func TestAtomicWriteFile_ReplacesExistingWholeFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store.json")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 4096)), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := AtomicWriteFile(path, []byte("short"), 0o600); err != nil {
		t.Fatalf("AtomicWriteFile: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "short" {
		t.Fatalf("stale bytes survived the replace: %q", got)
	}
}

func TestAtomicWriteFile_LeavesNoTempBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store.json")
	if err := AtomicWriteFile(path, []byte("ok"), 0o600); err != nil {
		t.Fatalf("AtomicWriteFile: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "store.json" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("directory should hold only the target, got %v", names)
	}
}

func TestAtomicWriteFile_MissingDirFailsWithoutTouchingTarget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope", "store.json")
	if err := AtomicWriteFile(path, []byte("ok"), 0o600); err == nil {
		t.Fatal("expected error when parent directory is missing")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("target must not exist after a failed write, stat err = %v", err)
	}
}
