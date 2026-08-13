/*
 * ChatCLI - Interop matrix tests: one fixture per agent-CLI dialect.
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 */
package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newInteropCatalog builds a catalog with project + home dirs rooted at
// temp locations, mirroring how each CLI actually lays its files out.
func newInteropCatalog(t *testing.T) (*Catalog, string, string) {
	t.Helper()
	project, home := t.TempDir(), t.TempDir()
	cat := NewCatalog(project, filepath.Join(home, ".chatcli", "commands"), nil, nil)
	cat.SetHomeDir(home)
	return cat, project, home
}

func TestInterop_GeminiTOML(t *testing.T) {
	cat, project, home := newInteropCatalog(t)
	write(t, filepath.Join(project, ".gemini", "commands", "git", "commit.toml"),
		"# Gemini custom command\ndescription = \"Writes a conventional commit\"\nprompt = \"\"\"\nWrite a commit message for: {{args}}\nGround it in !{git diff --staged}.\n\"\"\"\n")
	write(t, filepath.Join(home, ".gemini", "commands", "plan.toml"),
		"prompt = \"Make a plan for {{args}}\"\n")

	cmd := cat.Get("git:commit")
	if cmd == nil {
		t.Fatalf("project Gemini TOML must load with namespace; skipped=%v", cat.Skipped())
	}
	if cmd.Source != SourceGemini || cmd.Description != "Writes a conventional commit" {
		t.Errorf("gemini command mismatch: %+v", cmd)
	}
	expanded := Expand(cmd, "fix the parser", func(sh string) (string, bool) {
		if sh != "git diff --staged" {
			t.Errorf("inline exec extracted wrong command: %q", sh)
		}
		return "diff-output", true
	})
	if !strings.Contains(expanded, "Write a commit message for: fix the parser") ||
		!strings.Contains(expanded, "diff-output") {
		t.Errorf("gemini expansion failed: %q", expanded)
	}

	if global := cat.Get("plan"); global == nil || global.Source != SourceGemini {
		t.Errorf("global ~/.gemini command must load: %+v", global)
	}
}

func TestInterop_QwenTOML(t *testing.T) {
	cat, project, _ := newInteropCatalog(t)
	write(t, filepath.Join(project, ".qwen", "commands", "review.toml"),
		"description = 'Review code'\nprompt = '''Review {{args}} carefully.'''\n")

	cmd := cat.Get("review")
	if cmd == nil || cmd.Source != SourceQwen {
		t.Fatalf("qwen TOML must load; skipped=%v", cat.Skipped())
	}
	if got := Expand(cmd, "the diff", nil); got != "Review the diff carefully." {
		t.Errorf("qwen expansion mismatch: %q", got)
	}
}

func TestInterop_CodexPromptsGlobalTopLevelNamedArgs(t *testing.T) {
	cat, _, home := newInteropCatalog(t)
	write(t, filepath.Join(home, ".codex", "prompts", "draftpr.md"),
		"---\ndescription: Prep a branch and open a draft PR\nargument-hint: [FILES=<paths>] [PR_TITLE=\"<title>\"]\n---\nOpen a draft PR titled $PR_TITLE touching $FILES. Extra: $ARGUMENTS")
	// Codex scans only top-level files: a subdirectory must be ignored.
	write(t, filepath.Join(home, ".codex", "prompts", "nested", "hidden.md"), "never loads")

	cmd := cat.Get("draftpr")
	if cmd == nil || cmd.Source != SourceCodex {
		t.Fatalf("codex prompt must load; skipped=%v", cat.Skipped())
	}
	if cat.Get("nested:hidden") != nil || cat.Get("hidden") != nil {
		t.Error("codex subdirectories must be ignored (top-level-only contract)")
	}

	expanded := Expand(cmd, `FILES="src/a.ts src/b.ts" PR_TITLE="Add hero"`, nil)
	if !strings.Contains(expanded, "titled Add hero") || !strings.Contains(expanded, "touching src/a.ts src/b.ts") {
		t.Errorf("named-arg interpolation failed: %q", expanded)
	}
}

func TestInterop_CursorWindsurfOpencodeCopilot(t *testing.T) {
	cat, project, home := newInteropCatalog(t)
	write(t, filepath.Join(project, ".cursor", "commands", "lint-fix.md"), "Run the linter and fix findings.")
	write(t, filepath.Join(project, ".windsurf", "workflows", "release.md"),
		"---\ndescription: Release workflow\nauto_execute_steps: false\n---\nCut the release step by step.")
	write(t, filepath.Join(project, ".opencode", "commands", "component.md"),
		"---\ndescription: New component\nagent: coder\nsubtask: true\nmodel: claude-sonnet-5\n---\nCreate component $ARGUMENTS with output of !`ls src/components`.")
	write(t, filepath.Join(project, ".github", "prompts", "security-review.prompt.md"),
		"---\ndescription: Security review\nmode: agent\n---\nReview for security issues: $ARGUMENTS")
	write(t, filepath.Join(home, ".cursor", "commands", "personal.md"), "Personal cursor command.")

	cases := map[string]Source{
		"lint-fix":        SourceCursor,
		"release":         SourceWindsurf,
		"component":       SourceOpencode,
		"security-review": SourceCopilot,
		"personal":        SourceCursor,
	}
	for name, source := range cases {
		cmd := cat.Get(name)
		if cmd == nil {
			t.Errorf("%s must load; skipped=%v", name, cat.Skipped())
			continue
		}
		if cmd.Source != source {
			t.Errorf("%s: source %s, want %s", name, cmd.Source, source)
		}
	}
	// Foreign frontmatter keys (agent/subtask/mode/auto_execute_steps) are
	// tolerated; opencode's model maps to our hint.
	if cmd := cat.Get("component"); cmd.Model != "claude-sonnet-5" {
		t.Errorf("opencode model must map to the hint: %+v", cmd)
	}
	// opencode inline backtick exec goes through the gate.
	out := Expand(cat.Get("component"), "Button", func(sh string) (string, bool) {
		if sh != "ls src/components" {
			t.Errorf("backtick inline exec extracted %q", sh)
		}
		return "Button.tsx", true
	})
	if !strings.Contains(out, "Create component Button") || !strings.Contains(out, "Button.tsx") {
		t.Errorf("opencode expansion failed: %q", out)
	}
}

func TestInterop_PrecedenceNativeBeatsInterop(t *testing.T) {
	cat, project, _ := newInteropCatalog(t)
	write(t, filepath.Join(project, ".chatcli", "commands", "deploy.md"), "chatcli body")
	write(t, filepath.Join(project, ".gemini", "commands", "deploy.toml"), "prompt = \"gemini body\"")
	write(t, filepath.Join(project, ".cursor", "commands", "deploy.md"), "cursor body")

	if cmd := cat.Get("deploy"); cmd == nil || cmd.Source != SourceProject {
		t.Fatalf("native .chatcli must win the collision: %+v", cmd)
	}
}

func TestInterop_BrokenTOMLSurfacesInSkipped(t *testing.T) {
	cat, project, _ := newInteropCatalog(t)
	write(t, filepath.Join(project, ".gemini", "commands", "broken.toml"),
		"prompt = \"\"\"\nnever closed\n")

	if cat.Get("broken") != nil {
		t.Fatal("unterminated TOML must not load")
	}
	found := false
	for path, reason := range cat.Skipped() {
		if strings.Contains(path, "broken.toml") && strings.Contains(reason, "unterminated") {
			found = true
		}
	}
	if !found {
		t.Errorf("broken TOML must surface in diagnostics: %v", cat.Skipped())
	}
}

func TestInterop_InlineExecDeniedAndPreview(t *testing.T) {
	cmd := &Command{Content: "Context !{git status} end."}
	if lines := PreExecLines(cmd, ""); len(lines) != 1 || lines[0] != "git status" {
		t.Fatalf("inline commands must appear in the approval preview: %v", lines)
	}
	out := Expand(cmd, "", func(string) (string, bool) { return "", false })
	if !strings.Contains(out, deniedMarker) {
		t.Errorf("denied inline exec must leave the marker: %q", out)
	}
	// Line-leading inline form must resolve inline, never as a whole-line
	// shell command of literal braces.
	cmd = &Command{Content: "!{echo hi}"}
	out = Expand(cmd, "", func(sh string) (string, bool) {
		if sh != "echo hi" {
			t.Errorf("line-leading inline extracted %q", sh)
		}
		return "hi", true
	})
	if strings.TrimSpace(out) != "hi" {
		t.Errorf("line-leading inline expansion mismatch: %q", out)
	}
}

func TestInterop_HomeDirsHermetic(t *testing.T) {
	// Guard: a catalog rooted at temp dirs must never see the developer's
	// real command libraries even when they exist.
	cat, _, _ := newInteropCatalog(t)
	if home, _ := os.UserHomeDir(); home != "" {
		for _, cmd := range cat.List() {
			if strings.HasPrefix(cmd.Path, home) {
				t.Fatalf("real home leaked into a hermetic catalog: %s", cmd.Path)
			}
		}
	}
}
