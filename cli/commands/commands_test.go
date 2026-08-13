/*
 * ChatCLI - Slash command package tests.
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 */
package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newTestCatalog(t *testing.T, reserved ...string) (*Catalog, string, string) {
	t.Helper()
	project, global := t.TempDir(), t.TempDir()
	res := make(map[string]bool, len(reserved))
	for _, r := range reserved {
		res[r] = true
	}
	cat := NewCatalog(project, global, func(n string) bool { return res[n] }, nil)
	cat.SetHomeDir(t.TempDir()) // never read the developer's real interop dirs
	return cat, project, global
}

func TestParse_FrontmatterAndBody(t *testing.T) {
	cat, project, _ := newTestCatalog(t)
	write(t, filepath.Join(project, ".chatcli", "commands", "review-pr.md"),
		"---\ndescription: Review a PR\nargument-hint: <pr> [focus]\nmodel: claude-sonnet-5\neffort: high\nallowed-tools: read, search\n---\nReview PR $1 focusing on $ARGUMENTS.")

	cmd := cat.Get("review-pr")
	if cmd == nil {
		t.Fatal("command not loaded")
	}
	if cmd.Description != "Review a PR" || cmd.ArgumentHint != "<pr> [focus]" ||
		cmd.Model != "claude-sonnet-5" || cmd.Effort != "high" {
		t.Errorf("frontmatter mismatch: %+v", cmd)
	}
	if len(cmd.AllowedTools) != 2 || cmd.AllowedTools[0] != "read" || cmd.AllowedTools[1] != "search" {
		t.Errorf("allowed-tools mismatch: %v", cmd.AllowedTools)
	}
	if !strings.HasPrefix(cmd.Content, "Review PR $1") {
		t.Errorf("body mismatch: %q", cmd.Content)
	}
}

func TestParse_NoFrontmatterAndLongLine(t *testing.T) {
	cat, project, _ := newTestCatalog(t)
	// A single 100KB line — the skill loader's Scanner would silently drop
	// this file; the command parser must load it whole.
	long := strings.Repeat("x", 100*1024)
	write(t, filepath.Join(project, ".chatcli", "commands", "plain.md"), "Just do $ARGUMENTS.\n"+long)

	cmd := cat.Get("plain")
	if cmd == nil {
		t.Fatal("no-frontmatter file must load")
	}
	if !strings.Contains(cmd.Content, long) {
		t.Error("100KB line must survive parsing intact")
	}
}

func TestParse_Rejections(t *testing.T) {
	cat, project, _ := newTestCatalog(t)
	base := filepath.Join(project, ".chatcli", "commands")
	write(t, filepath.Join(base, "unterminated.md"), "---\ndescription: broken\nno closing fence")
	write(t, filepath.Join(base, "bad name!.md"), "body")
	write(t, filepath.Join(base, "huge.md"), strings.Repeat("y", maxCommandFileBytes+1))

	if got := cat.List(); len(got) != 0 {
		t.Fatalf("broken files must be skipped, got %v", got)
	}
}

func TestPrecedence_ProjectBeatsGlobalBeatsInterop(t *testing.T) {
	cat, project, global := newTestCatalog(t)
	write(t, filepath.Join(project, ".claude", "commands", "deploy.md"), "claude interop")
	write(t, filepath.Join(global, "deploy.md"), "global body")
	write(t, filepath.Join(project, ".chatcli", "commands", "deploy.md"), "project body")
	write(t, filepath.Join(project, ".devin", "workflows", "triage.md"), "devin body")

	if cmd := cat.Get("deploy"); cmd == nil || cmd.Content != "project body" || cmd.Source != SourceProject {
		t.Fatalf("project must win: %+v", cmd)
	}
	if cmd := cat.Get("triage"); cmd == nil || cmd.Source != SourceDevin {
		t.Fatalf("devin workflows must load: %+v", cmd)
	}
}

func TestInteropOnly_ClaudeCommandsLoad(t *testing.T) {
	cat, project, _ := newTestCatalog(t)
	write(t, filepath.Join(project, ".claude", "commands", "frontend", "deploy.md"),
		"---\ndescription: Deploy FE\n---\nDeploy $1 to $2")

	cmd := cat.Get("frontend:deploy")
	if cmd == nil {
		t.Fatal("namespaced interop command must load")
	}
	if cmd.Namespace != "frontend" || cmd.Source != SourceClaude {
		t.Errorf("namespace/source mismatch: %+v", cmd)
	}
}

func TestReservedNamesRefused(t *testing.T) {
	cat, project, _ := newTestCatalog(t, "session")
	write(t, filepath.Join(project, ".chatcli", "commands", "session.md"), "hijack attempt")
	write(t, filepath.Join(project, ".chatcli", "commands", "ok.md"), "fine")

	if cat.Get("session") != nil {
		t.Fatal("reserved name must never load")
	}
	if cat.Get("ok") == nil {
		t.Fatal("non-reserved sibling must load")
	}
	if _, refused := cat.Refused()["session"]; !refused {
		t.Error("refusal must be recorded for diagnostics")
	}
}

func TestFingerprint_CacheAndInvalidation(t *testing.T) {
	cat, project, _ := newTestCatalog(t)
	path := filepath.Join(project, ".chatcli", "commands", "a.md")
	write(t, path, "v1 $ARGUMENTS")

	if cat.Get("a").Content != "v1 $ARGUMENTS" {
		t.Fatal("initial load")
	}
	// Rewrite with different size → stat fingerprint flips → re-scan.
	write(t, path, "v2 rewritten $ARGUMENTS")
	if got := cat.Get("a").Content; got != "v2 rewritten $ARGUMENTS" {
		t.Fatalf("edit must invalidate the snapshot, got %q", got)
	}
}

func TestInterpolate(t *testing.T) {
	cases := []struct{ body, args, want string }{
		{"PR $1 focus $2", "1326 security", "PR 1326 focus security"},
		{"all: $ARGUMENTS", "a b c", "all: a b c"},
		{"missing $3 end", "one two", "missing  end"},
		{"cost $$5 and $UNKNOWN", "x", "cost $5 and $UNKNOWN"},
		{"unicode $1", "ação", "unicode ação"},
		{"gemini: {{args}}!", "make a plan", "gemini: make a plan!"},
		{"title $TITLE files $FILES", `TITLE="Add hero" FILES=src/a.ts`, "title Add hero files src/a.ts"},
		{"pos $1 named $KEY pos $2", `a KEY=v b`, "pos a named v pos b"},
		{"unset $NOPE stays", "x", "unset $NOPE stays"},
	}
	for _, tc := range cases {
		if got := interpolate(tc.body, tc.args); got != tc.want {
			t.Errorf("interpolate(%q, %q) = %q, want %q", tc.body, tc.args, got, tc.want)
		}
	}
}

func TestExpand_PreExecGatedAndFenceSafe(t *testing.T) {
	cmd := &Command{Content: "Context:\n! git status\n```bash\n! not-a-preexec\n```\nDone $1"}

	// Runner approves: output is embedded.
	out := Expand(cmd, "arg1", func(sh string) (string, bool) {
		if sh != "git status" {
			t.Errorf("unexpected pre-exec command %q", sh)
		}
		return "clean tree", true
	})
	if !strings.Contains(out, "clean tree") || !strings.Contains(out, "Done arg1") {
		t.Errorf("approved pre-exec must embed output: %q", out)
	}
	if !strings.Contains(out, "! not-a-preexec") {
		t.Error("fenced ! lines must pass through untouched")
	}

	// Runner denies: marker replaces the line.
	out = Expand(cmd, "", func(string) (string, bool) { return "", false })
	if !strings.Contains(out, deniedMarker) {
		t.Errorf("denied pre-exec must leave an explicit marker: %q", out)
	}

	// No runner (surface without a gate): fail-safe marker too.
	out = Expand(cmd, "", nil)
	if !strings.Contains(out, deniedMarker) || strings.Contains(out, "clean tree") {
		t.Errorf("nil runner must never execute: %q", out)
	}

	if lines := PreExecLines(cmd, ""); len(lines) != 1 || lines[0] != "git status" {
		t.Errorf("PreExecLines must list exactly the gated commands: %v", lines)
	}
}
