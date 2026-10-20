/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package atrest

import (
	"path/filepath"
	"testing"
)

// A store sealed under ~/.chatcli opens from the same place under a custom
// state directory: the binding is the path relative to the root, so a
// state directory copied between roots keeps working.
func TestSealAt_CustomStateRootBindsTheRelativePath(t *testing.T) {
	t.Setenv(EnvKey, "0123456789abcdef0123456789abcdef-long-random-key")
	root := filepath.Join(t.TempDir(), "bench-state")

	t.Setenv("CHATCLI_STATE_DIR", "")
	sealed, err := SealAt("/home/u/.chatcli/memory/vindex/work.json", []byte("payload"))
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("CHATCLI_STATE_DIR", root)
	moved := filepath.Join(root, "memory", "vindex", "work.json")
	if plain, err := OpenAt(moved, sealed); err != nil || string(plain) != "payload" {
		t.Fatalf("the same store under the custom root must open: %v", err)
	}
	if _, err := OpenAt(filepath.Join(root, "other", "vindex", "work.json"), sealed); err == nil {
		t.Fatal("a store moved to another place under the root must not open")
	}

	// Sealed under the custom root, it round-trips there.
	inRoot, err := SealAt(filepath.Join(root, "memory", "facts.json"), []byte("facts"))
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := OpenAt(filepath.Join(root, "memory", "facts.json"), inRoot); err != nil || string(plain) != "facts" {
		t.Fatalf("round trip under the custom root: %v", err)
	}
}
