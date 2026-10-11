//go:build !windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package hub

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// The hub database holds every channel's conversation: the database, its
// WAL and its shared-memory file must be owner-only even under the usual
// 022 umask, on a fresh database and on one an older version left 0644.
func TestOpenSQLiteStore_FilesAreOwnerOnly(t *testing.T) {
	old := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(old) })
	ctx := context.Background()

	check := func(t *testing.T, path string) {
		t.Helper()
		for _, suffix := range []string{"", "-wal", "-shm"} {
			fi, err := os.Stat(path + suffix)
			if err != nil {
				t.Fatalf("%s%s: %v", filepath.Base(path), suffix, err)
			}
			if m := fi.Mode().Perm(); m != 0o600 {
				t.Errorf("%s%s mode = %v, want 0600", filepath.Base(path), suffix, m)
			}
		}
	}

	t.Run("fresh", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "hub.db")
		s, err := OpenSQLiteStore(ctx, path, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s.Close() }()
		if _, err := s.Resolve(ctx, "principal-a"); err != nil {
			t.Fatal(err)
		}
		check(t, path)
	})

	t.Run("existing loose", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "hub.db")
		s, err := OpenSQLiteStore(ctx, path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Resolve(ctx, "principal-a"); err != nil {
			t.Fatal(err)
		}
		_ = s.Close()
		for _, suffix := range []string{"", "-wal", "-shm"} {
			if _, err := os.Stat(path + suffix); err == nil {
				if err := os.Chmod(path+suffix, 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}
		s, err = OpenSQLiteStore(ctx, path, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s.Close() }()
		if _, err := s.Resolve(ctx, "principal-b"); err != nil {
			t.Fatal(err)
		}
		check(t, path)
	})
}
