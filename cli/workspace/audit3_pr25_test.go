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

	"go.uber.org/zap"
)

func writeInstrFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestInstructions_ImportsStayInsideTheWorkspace(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	global := t.TempDir()
	writeInstrFile(t, filepath.Join(outside, "secret.md"), "SECRET NOTES")
	writeInstrFile(t, filepath.Join(ws, "docs", "style.md"), "STYLE RULES")
	_ = os.Symlink(filepath.Join(outside, "secret.md"), filepath.Join(ws, "link.md"))
	writeInstrFile(t, filepath.Join(ws, "AGENTS.md"), "Project rules\n@docs/style.md\n@"+filepath.Join(outside, "secret.md")+"\n@link.md\n@~/nowhere.md\n")
	bl := NewBootstrapLoader(ws, global, zap.NewNop())
	doc, ok := bl.loadInstructionHierarchy()
	if !ok {
		t.Fatal("instructions must load")
	}
	if !strings.Contains(doc, "STYLE RULES") {
		t.Fatal("in-tree import must expand")
	}
	if strings.Contains(doc, "SECRET NOTES") {
		t.Fatal("out-of-tree imports (absolute or through a symlink) must be skipped")
	}
}

func TestInstructions_OverrideLocalAndGlobalMerge(t *testing.T) {
	ws := t.TempDir()
	global := t.TempDir()
	writeInstrFile(t, filepath.Join(global, "AGENTS.md"), "GLOBAL PREFS")
	writeInstrFile(t, filepath.Join(ws, "AGENTS.md"), "SHARED PROJECT")
	writeInstrFile(t, filepath.Join(ws, "AGENTS.local.md"), "MY LOCAL ADDITIONS")
	bl := NewBootstrapLoader(ws, global, zap.NewNop())
	doc, _ := bl.loadInstructionHierarchy()
	for _, want := range []string{"GLOBAL PREFS", "SHARED PROJECT", "MY LOCAL ADDITIONS"} {
		if !strings.Contains(doc, want) {
			t.Fatalf("missing %q in %q", want, doc)
		}
	}
	if strings.Index(doc, "GLOBAL PREFS") > strings.Index(doc, "SHARED PROJECT") {
		t.Fatal("the global file merges first")
	}
	writeInstrFile(t, filepath.Join(ws, "AGENTS.override.md"), "OVERRIDE WINS")
	bl2 := NewBootstrapLoader(ws, global, zap.NewNop())
	doc2, _ := bl2.loadInstructionHierarchy()
	if !strings.Contains(doc2, "OVERRIDE WINS") || strings.Contains(doc2, "SHARED PROJECT") {
		t.Fatalf("AGENTS.override.md must replace AGENTS.md: %q", doc2)
	}
}

func TestInstructions_PerFileImportCap(t *testing.T) {
	ws := t.TempDir()
	writeInstrFile(t, filepath.Join(ws, "big.md"), strings.Repeat("x", importFileMaxBytes*3))
	writeInstrFile(t, filepath.Join(ws, "AGENTS.md"), "@big.md\n")
	bl := NewBootstrapLoader(ws, "", zap.NewNop())
	doc, _ := bl.loadInstructionHierarchy()
	if len(doc) > importFileMaxBytes+512 {
		t.Fatalf("one import must be capped on its own: %d bytes", len(doc))
	}
}

func TestRules_ReadClaudeRulesToo(t *testing.T) {
	ws := t.TempDir()
	writeInstrFile(t, filepath.Join(ws, ".claude", "rules", "go.md"), "Use gofmt")
	writeInstrFile(t, filepath.Join(ws, ".chatcli", "rules", "go.md"), "Use gofmt and govet")
	rl := NewRulesLoader(ws, t.TempDir(), zap.NewNop())
	out := rl.LoadMatchingRules(nil)
	if !strings.Contains(out, "Use gofmt and govet") || strings.Contains(out, "Use gofmt\n") {
		t.Fatalf(".chatcli/rules must win over .claude/rules of the same name: %q", out)
	}
	ws2 := t.TempDir()
	writeInstrFile(t, filepath.Join(ws2, ".claude", "rules", "py.md"), "Use ruff")
	if out := NewRulesLoader(ws2, t.TempDir(), zap.NewNop()).LoadMatchingRules(nil); !strings.Contains(out, "Use ruff") {
		t.Fatalf(".claude/rules alone must load: %q", out)
	}
}
