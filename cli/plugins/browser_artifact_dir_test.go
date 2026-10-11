//go:build !windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"os"
	"path/filepath"
	"testing"
)

// Files @browser saves without a path go to an owner-only directory under
// the state root, not a fixed name in the shared temp directory.
func TestBrowserArtifactDir_IsPrivateUnderTheStateRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir, err := browserArtifactDir()
	if err != nil || dir != filepath.Join(home, ".chatcli", "browser") {
		t.Fatalf("dir = %s, err = %v", dir, err)
	}
	fi, err := os.Lstat(dir)
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("lstat = %v, %v", fi, err)
	}
}
