/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/diillson/chatcli/cli/workspace/memory"
)

func TestRuneSafeCuts_MemoryPaths(t *testing.T) {
	s := strings.Repeat("é", 700) // 1400 bytes of 2-byte runes
	head := truncateRunesafe(s, 1200)
	tail := tailRunesafe(s, 200)
	if !utf8.ValidString(head) || !utf8.ValidString(tail) || len(head) > 1200+utf8.UTFMax || len(tail) > 200 {
		t.Fatalf("head=%d tail=%d valid=%v/%v", len(head), len(tail), utf8.ValidString(head), utf8.ValidString(tail))
	}
	if tailRunesafe("short", 200) != "short" {
		t.Fatal("short input untouched")
	}
}

func TestMemoryProvider_ExclusiveValue(t *testing.T) {
	if s, ok := extensionTarget("mcp-only:memsvc"); !ok || s != "memsvc" {
		t.Fatalf("mcp-only must select the server: %q %v", s, ok)
	}
	if !memoryProviderExclusive("MCP-ONLY:memsvc") || memoryProviderExclusive("mcp:memsvc") {
		t.Fatal("exclusive detection")
	}
	if extensionStatus("mcp-only:memsvc") != "mcp-only:memsvc" || extensionStatus("provider") != "provider" || extensionStatus("") != "builtin" {
		t.Fatal("status rendering")
	}
}

func TestSameProject_GitWorktreesAreOneProject(t *testing.T) {
	root := t.TempDir()
	main := filepath.Join(root, "repo")
	common := filepath.Join(main, ".git")
	if err := os.MkdirAll(filepath.Join(common, "worktrees", "feature"), 0o700); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(root, "repo-feature")
	if err := os.MkdirAll(wt, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+filepath.Join(common, "worktrees", "feature")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !memory.SameProject(main, wt) {
		t.Fatal("a git worktree and its main checkout are one project")
	}
	other := filepath.Join(root, "other")
	_ = os.MkdirAll(filepath.Join(other, ".git"), 0o700)
	if memory.SameProject(main, other) {
		t.Fatal("distinct repositories stay distinct")
	}
}

func TestBootstrapCard_RefreshesWhenItWasEmpty(t *testing.T) {
	c := &ChatCLI{}
	st := c.bootstrapCardState()
	st.once.Do(func() {}) // computed empty (fresh install)
	c.refreshBootstrapCard()
	if c.bootstrapCard == st {
		t.Fatal("an empty card must be rebuilt at the next boundary")
	}
	filled := c.bootstrapCardState()
	filled.once.Do(func() { filled.chat = "card" })
	c.refreshBootstrapCard()
	if c.bootstrapCard != filled {
		t.Fatal("a filled card is kept")
	}
}
