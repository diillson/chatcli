/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestInstructionHierarchy_RootToCwdWithFallbacks(t *testing.T) {
	root := t.TempDir()
	global := t.TempDir()
	writeFile(t, filepath.Join(global, "AGENTS.md"), "GLOBAL RULES")
	writeFile(t, filepath.Join(root, "AGENTS.md"), "ROOT RULES")
	writeFile(t, filepath.Join(root, "services", "CLAUDE.md"), "SERVICE RULES (claude fallback)")
	writeFile(t, filepath.Join(root, "services", "api", "CHATCLI.md"), "API RULES")
	writeFile(t, filepath.Join(root, "services", "api", "CLAUDE.md"), "must not win over CHATCLI.md")
	bl := NewBootstrapLoader(root, global, zap.NewNop())
	bl.SetWorkingDir(filepath.Join(root, "services", "api", "handlers"))
	out := bl.LoadBootstrapContent()
	for _, want := range []string{"ROOT RULES", "SERVICE RULES", "API RULES", "<!-- services/CLAUDE.md -->", "<!-- services/api/CHATCLI.md -->"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "must not win") {
		t.Fatalf("CHATCLI.md must beat CLAUDE.md:\n%s", out)
	}
	// The global file is always merged, first (Claude Code semantics); it
	// used to be a fallback only when the project had none.
	if !strings.Contains(out, "GLOBAL RULES") || strings.Index(out, "GLOBAL RULES") > strings.Index(out, "ROOT RULES") {
		t.Fatalf("global file must merge first:\n%s", out)
	}
	if strings.Index(out, "ROOT RULES") > strings.Index(out, "API RULES") {
		t.Fatal("root file must come first (least specific → most specific)")
	}
	// cwd outside the workspace: root only.
	bl.SetWorkingDir(t.TempDir())
	if out := bl.LoadBootstrapContent(); !strings.Contains(out, "ROOT RULES") || strings.Contains(out, "API RULES") {
		t.Fatalf("cwd outside the workspace must yield the root file only:\n%s", out)
	}
	// No project file at all: the global one.
	empty := t.TempDir()
	bl2 := NewBootstrapLoader(empty, global, zap.NewNop())
	if out := bl2.LoadBootstrapContent(); !strings.Contains(out, "GLOBAL RULES") {
		t.Fatalf("global fallback:\n%s", out)
	}
}

func TestExpandImports_HopsCyclesFencesAndMissing(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "AGENTS.md"), "TOP\n@docs/style.md\n@import docs/missing.md\n```\n@docs/style.md\n```\n@AGENTS.md")
	writeFile(t, filepath.Join(root, "docs", "style.md"), "STYLE\n@deeper/one.md")
	writeFile(t, filepath.Join(root, "docs", "deeper", "one.md"), "ONE\n@two.md")
	writeFile(t, filepath.Join(root, "docs", "deeper", "two.md"), "TWO\n@three.md")
	writeFile(t, filepath.Join(root, "docs", "deeper", "three.md"), "THREE\n@four.md")
	writeFile(t, filepath.Join(root, "docs", "deeper", "four.md"), "FOUR (fifth hop, must not appear)")
	bl := NewBootstrapLoader(root, "", zap.NewNop())
	bl.SetWorkingDir(root)
	out := bl.LoadBootstrapContent()
	for _, want := range []string{"TOP", "STYLE", "ONE", "TWO", "THREE", "<!-- import style.md -->", "@import docs/missing.md"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "FOUR") {
		t.Fatal("imports stop after four hops")
	}
	if strings.Count(out, "STYLE") != 1 {
		t.Fatalf("the fenced @ line must stay literal and a file imports once:\n%s", out)
	}
	if strings.Count(out, "TOP") != 1 {
		t.Fatal("a self-import must not recurse")
	}
	// Imported files participate in staleness.
	if bl.IsStale() {
		t.Fatal("fresh")
	}
	writeFile(t, filepath.Join(root, "docs", "style.md"), "STYLE v2")
	stale := false
	for i := 0; i < 20 && !stale; i++ {
		// mtime granularity: bump until the stat differs
		_ = os.Chtimes(filepath.Join(root, "docs", "style.md"), nowPlus(i), nowPlus(i))
		stale = bl.IsStale()
	}
	if !stale {
		t.Fatal("an edited imported file must mark the cache stale")
	}
}

func TestCapInstructionDoc(t *testing.T) {
	doc := strings.Repeat("line of instructions\n", 3000) // ~63 KiB
	out := capInstructionDoc(doc, InstructionDocMaxBytes)
	if len(out) > InstructionDocMaxBytes+128 || !strings.Contains(out, "truncated at 32 KiB") {
		t.Fatalf("cap: len=%d", len(out))
	}
	if capInstructionDoc("short", InstructionDocMaxBytes) != "short" {
		t.Fatal("under the cap: untouched")
	}
}

func TestOtherBootstrapFilesExpandImports(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "SOUL.md"), "SOUL\n@persona/voice.md")
	writeFile(t, filepath.Join(root, "persona", "voice.md"), "VOICE")
	bl := NewBootstrapLoader(root, t.TempDir(), zap.NewNop())
	if out := bl.LoadBootstrapContent(); !strings.Contains(out, "VOICE") {
		t.Fatalf("SOUL.md imports:\n%s", out)
	}
}

func nowPlus(i int) time.Time { return time.Now().Add(time.Duration(i+1) * time.Second) }
