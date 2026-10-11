//go:build !windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package browser

import (
	"os"
	"path/filepath"
	"testing"
)

// A symbolic link planted where the PDF goes must be replaced, never
// followed into the file it points at.
func TestWriteArtifactFile_ReplacesASymlinkInsteadOfFollowingIt(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim.txt")
	if err := os.WriteFile(victim, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "page.pdf")
	if err := os.Symlink(victim, target); err != nil {
		t.Fatal(err)
	}
	if err := writeArtifactFile(target, []byte("%PDF-fake")); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(victim); string(got) != "original" {
		t.Fatalf("the write followed the symlink: victim = %q", got)
	}
	fi, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink != 0 || fi.Mode().Perm() != 0o600 {
		t.Fatalf("target mode = %v, want a regular 0600 file", fi.Mode())
	}
	if got, _ := os.ReadFile(target); string(got) != "%PDF-fake" {
		t.Fatalf("target content = %q", got)
	}

	// A missing parent is created owner-only.
	nested := filepath.Join(dir, "new", "page.pdf")
	if err := writeArtifactFile(nested, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(filepath.Dir(nested)); fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("parent mode = %v, want owner-only", fi.Mode().Perm())
	}
}
