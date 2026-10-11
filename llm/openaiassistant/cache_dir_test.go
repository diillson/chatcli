//go:build !windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package openaiassistant

import (
	"os"
	"path/filepath"
	"testing"
)

// The assistant cache lives in an owner-only directory under the state
// root; a fixed name in the shared temp directory could be pre-created or
// symlinked by another local user.
func TestAssistantCacheDir_IsPrivateUnderTheStateRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir, err := assistantCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".chatcli", assistantCacheDirName); dir != want {
		t.Fatalf("dir = %s, want %s", dir, want)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("mode = %v, want 0700", fi.Mode().Perm())
	}
}
