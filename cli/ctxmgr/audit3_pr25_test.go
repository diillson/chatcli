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
)

func TestWatchDirsForCapped_HonoursGitignoreAndTheCap(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"src/a", "src/b", "build/out", "vendor2/x", "docs"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("# noise\nbuild/\nvendor*\n!keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dirs, truncated := watchDirsForCapped([]string{root}, 0)
	if truncated {
		t.Fatal("no cap → not truncated")
	}
	joined := map[string]bool{}
	for _, d := range dirs {
		joined[d] = true
	}
	if joined[filepath.Join(root, "build")] || joined[filepath.Join(root, "build", "out")] || joined[filepath.Join(root, "vendor2")] {
		t.Fatalf("gitignored directories must not be watched: %v", dirs)
	}
	if !joined[filepath.Join(root, "src", "a")] || !joined[filepath.Join(root, "docs")] {
		t.Fatalf("ordinary directories must be watched: %v", dirs)
	}
	capped, truncated := watchDirsForCapped([]string{root}, 2)
	if len(capped) != 2 || !truncated {
		t.Fatalf("cap must stop the walk and report it: %d %v", len(capped), truncated)
	}
}
