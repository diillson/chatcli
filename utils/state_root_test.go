//go:build !windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package utils

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func modeOf(t *testing.T, path string) fs.FileMode {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

// withUmask runs the test under a permissive umask, the common default,
// so the assertions prove the code asks for owner-only modes itself.
func withUmask(t *testing.T, mask int) {
	t.Helper()
	old := syscall.Umask(mask)
	t.Cleanup(func() { syscall.Umask(old) })
}

func TestEnsureStateRoot_FirstRunIsOwnerOnly(t *testing.T) {
	withUmask(t, 0o022)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("LOG_FILE", "")
	t.Setenv("CHATCLI_LOG_FILE", "")
	t.Setenv("CHATCLI_HUB_DB", "")

	if err := EnsureStateRoot(); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, ".chatcli")
	if m := modeOf(t, root); m != 0o700 {
		t.Fatalf("state root mode = %v, want 0700", m)
	}

	// The logger is the first writer on a fresh install; its file lands in
	// the private root and is itself owner-only.
	logger, err := InitializeLogger()
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("first run")
	_ = logger.Sync()
	if m := modeOf(t, root); m != 0o700 {
		t.Fatalf("state root mode after logger init = %v, want 0700", m)
	}
	if m := modeOf(t, filepath.Join(root, "app.log")); m&0o077 != 0 {
		t.Fatalf("app.log mode = %v, want no group/other bits", m)
	}
}

func TestEnsureStateRoot_RepairsLooseModes(t *testing.T) {
	withUmask(t, 0o022)
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, ".chatcli")
	for _, d := range []string{root, filepath.Join(root, "sessions"), filepath.Join(root, "memory"), filepath.Join(root, "transcripts")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := []string{"hub.db", "hub.db-wal", "hub.db-shm", "app.log", ".chatcli_history", "auth-profiles.json"}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(root, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A read-only file keeps its narrower mode: repairs only remove bits.
	readOnly := filepath.Join(root, "board.json")
	if err := os.WriteFile(readOnly, []byte("x"), 0o444); err != nil {
		t.Fatal(err)
	}

	// A symbolic link inside the root must never be followed.
	outside := t.TempDir()
	if err := os.Chmod(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(outside, "shared.txt")
	if err := os.WriteFile(outsideFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "ccr")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(root, "linked.json")); err != nil {
		t.Fatal(err)
	}

	// A hub database configured outside the root is repaired too.
	customDir := t.TempDir()
	customDB := filepath.Join(customDir, "custom-hub.db")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.WriteFile(customDB+suffix, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CHATCLI_HUB_DB", customDB)

	if err := EnsureStateRoot(); err != nil {
		t.Fatal(err)
	}

	for _, d := range []string{root, filepath.Join(root, "sessions"), filepath.Join(root, "memory"), filepath.Join(root, "transcripts")} {
		if m := modeOf(t, d); m != 0o700 {
			t.Errorf("%s mode = %v, want 0700", d, m)
		}
	}
	for _, f := range files {
		if m := modeOf(t, filepath.Join(root, f)); m != 0o600 {
			t.Errorf("%s mode = %v, want 0600", f, m)
		}
	}
	if m := modeOf(t, readOnly); m != 0o400 {
		t.Errorf("read-only file mode = %v, want 0400", m)
	}
	if m := modeOf(t, outside); m != 0o755 {
		t.Errorf("a directory behind a symlink was changed: %v", m)
	}
	if m := modeOf(t, outsideFile); m != 0o644 {
		t.Errorf("a file behind a symlink was changed: %v", m)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if m := modeOf(t, customDB+suffix); m != 0o600 {
			t.Errorf("custom hub db%s mode = %v, want 0600", suffix, m)
		}
	}
	if _, err := os.Stat(customDB + "-journal"); err == nil {
		t.Error("the repair created a file that did not exist")
	}

	// Idempotent: a second start finds nothing to change.
	if err := EnsureStateRoot(); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureStateRoot_RefusesARootThatIsNotADirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, ".chatcli"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsureStateRoot(); err == nil {
		t.Fatal("a regular file at the state root was accepted")
	}
}

func TestPrivateStateDir_OwnerOnlyAndNoSymlinks(t *testing.T) {
	withUmask(t, 0o022)
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir, err := PrivateStateDir("browser")
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(home, ".chatcli", "browser") {
		t.Fatalf("dir = %s", dir)
	}
	if m := modeOf(t, dir); m != 0o700 {
		t.Fatalf("mode = %v, want 0700", m)
	}

	// An existing loose directory is tightened.
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := PrivateStateDir("browser"); err != nil {
		t.Fatal(err)
	}
	if m := modeOf(t, dir); m != 0o700 {
		t.Fatalf("loose dir not repaired: %v", m)
	}

	// A planted symlink is refused, never used.
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(home, ".chatcli", "agent-logs")); err != nil {
		t.Fatal(err)
	}
	if _, err := PrivateStateDir("agent-logs"); err == nil {
		t.Fatal("a symlinked state directory was accepted")
	}

	for _, bad := range []string{"", ".", "..", "a/b", "../x"} {
		if _, err := PrivateStateDir(bad); err == nil {
			t.Errorf("accepted name %q", bad)
		}
	}
}

func TestRestrictSQLiteFiles_CreatesOwnerOnly(t *testing.T) {
	withUmask(t, 0o022)
	db := filepath.Join(t.TempDir(), "hub.db")
	if err := RestrictSQLiteFiles(db); err != nil {
		t.Fatal(err)
	}
	if m := modeOf(t, db); m != 0o600 {
		t.Fatalf("new db mode = %v, want 0600", m)
	}
	for _, mem := range []string{":memory:", "file::memory:?cache=shared", ""} {
		if err := RestrictSQLiteFiles(mem); err != nil {
			t.Errorf("%q: %v", mem, err)
		}
	}
}
