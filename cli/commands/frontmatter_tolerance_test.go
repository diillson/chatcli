/*
 * ChatCLI - Frontmatter tolerance tests (real-world YAML shapes).
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 */
package commands

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestFrontmatter_BareBracketHintLoads is the exact field bug report: a user
// copied `argument-hint: [dias]` (the Claude Code documented shape) and the
// command silently vanished — YAML parses a bare bracketed token as a
// SEQUENCE, and the strict string field failed the whole unmarshal.
func TestFrontmatter_BareBracketHintLoads(t *testing.T) {
	cat, project, _ := newTestCatalog(t)
	write(t, filepath.Join(project, ".chatcli", "commands", "standup.md"),
		"---\ndescription: Resumo de standup do trabalho recente\nargument-hint: [dias]\n---\n! git log --oneline\nCom base nos commits acima, escreva um resumo.")

	cmd := cat.Get("standup")
	if cmd == nil {
		t.Fatalf("command with a bare-bracket argument-hint must load; skipped=%v", cat.Skipped())
	}
	if cmd.ArgumentHint != "[dias]" {
		t.Errorf("hint must render back as the literal the author wrote, got %q", cmd.ArgumentHint)
	}
	if cmd.Description != "Resumo de standup do trabalho recente" {
		t.Errorf("description mismatch: %q", cmd.Description)
	}
}

func TestFrontmatter_FlexShapes(t *testing.T) {
	cat, project, _ := newTestCatalog(t)
	write(t, filepath.Join(project, ".chatcli", "commands", "flex.md"),
		"---\ndescription: [multi, word, hint]\nargument-hint: 42\nmodel: claude-sonnet-5\n---\nbody")

	cmd := cat.Get("flex")
	if cmd == nil {
		t.Fatalf("flexible frontmatter must load; skipped=%v", cat.Skipped())
	}
	if cmd.Description != "[multi, word, hint]" {
		t.Errorf("sequence must re-render as the bracketed literal, got %q", cmd.Description)
	}
	if cmd.ArgumentHint != "42" {
		t.Errorf("numeric scalar must coerce to string, got %q", cmd.ArgumentHint)
	}
}

func TestSkippedDiagnostics_SurfaceParseFailures(t *testing.T) {
	cat, project, _ := newTestCatalog(t)
	write(t, filepath.Join(project, ".chatcli", "commands", "broken.md"),
		"---\ndescription: broken\nno closing fence")
	write(t, filepath.Join(project, ".chatcli", "commands", "fine.md"), "works")

	if cat.Get("fine") == nil {
		t.Fatal("healthy sibling must load")
	}
	skipped := cat.Skipped()
	if len(skipped) != 1 {
		t.Fatalf("expected exactly the broken file in diagnostics, got %v", skipped)
	}
	for path, reason := range skipped {
		if !strings.Contains(path, "broken.md") || !strings.Contains(reason, "frontmatter") {
			t.Errorf("diagnostic must name the file and the reason: %s → %s", path, reason)
		}
	}
}
