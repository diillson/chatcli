/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseDocsFlattenFrontMatter(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantTitle string
		wantHasFM bool
	}{
		{
			name:      "yaml quoted title",
			input:     "---\ntitle: \"Getting Started\"\n---\nbody",
			wantTitle: "Getting Started",
			wantHasFM: true,
		},
		{
			name:      "yaml unquoted title",
			input:     "---\ntitle: Getting Started\ndescription: x\n---\nbody",
			wantTitle: "Getting Started",
			wantHasFM: true,
		},
		{
			name:      "toml quoted title",
			input:     "+++\ntitle = \"Config\"\n+++\nbody",
			wantTitle: "Config",
			wantHasFM: true,
		},
		{
			name:      "toml unquoted title",
			input:     "+++\ntitle = Config\n+++\nbody",
			wantTitle: "Config",
			wantHasFM: true,
		},
		{
			name:      "no front matter",
			input:     "# Heading\nbody",
			wantTitle: "",
			wantHasFM: false,
		},
		{
			name:      "unterminated fence",
			input:     "---\ntitle: Broken\nbody",
			wantTitle: "",
			wantHasFM: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			title, body, hasFM := parseDocsFlattenFrontMatter(tt.input)
			if title != tt.wantTitle {
				t.Errorf("title = %q, want %q", title, tt.wantTitle)
			}
			if hasFM != tt.wantHasFM {
				t.Errorf("hasFM = %v, want %v", hasFM, tt.wantHasFM)
			}
			if hasFM && !strings.Contains(body, "body") {
				t.Errorf("body lost content: %q", body)
			}
		})
	}
}

func TestChunkMarkdownHeadingAware(t *testing.T) {
	doc := "# A\n" + strings.Repeat("aaaa aaaa\n", 5) +
		"# B\n" + strings.Repeat("bbbb bbbb\n", 5) +
		"# C\n" + strings.Repeat("cccc cccc\n", 5)
	chunks := chunkMarkdown(doc, 80)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	// Heading-aware: every chunk must start at a section boundary, never
	// mid-section (a chunk's first line is a heading).
	for i, c := range chunks {
		first := strings.SplitN(c, "\n", 2)[0]
		if !strings.HasPrefix(first, "# ") {
			t.Errorf("chunk %d does not start at a heading: %q", i, first)
		}
	}
}

func TestChunkMarkdownFenceProtectsHeadings(t *testing.T) {
	doc := "# Real\nintro\n```sh\n# not a heading\necho hi\n```\ntail\n# Next\nmore"
	sections := splitMarkdownSections(doc)
	if len(sections) != 2 {
		t.Fatalf("expected 2 sections (fenced # ignored), got %d: %#v", len(sections), sections)
	}
	if !strings.Contains(sections[0], "# not a heading") {
		t.Errorf("fenced comment left its section: %#v", sections)
	}
}

func TestChunkMarkdownOversizeSectionFallsBack(t *testing.T) {
	doc := "# Big\n" + strings.Repeat("line line line\n", 50)
	chunks := chunkMarkdown(doc, 100)
	if len(chunks) < 2 {
		t.Fatalf("oversize section should split, got %d chunk(s)", len(chunks))
	}
	for i, c := range chunks {
		if len(c) > 100 {
			t.Errorf("chunk %d exceeds maxChars: %d", i, len(c))
		}
	}
}

func TestChunkMarkdownNoSplit(t *testing.T) {
	if got := chunkMarkdown("small", 0); len(got) != 1 || got[0] != "small" {
		t.Errorf("maxChars=0 must not split: %#v", got)
	}
	if got := chunkMarkdown("   \n  ", 100); got != nil {
		t.Errorf("blank content must yield no chunks: %#v", got)
	}
}

func TestMatchDocsGlob(t *testing.T) {
	tests := []struct {
		pattern string
		path    string
		want    bool
	}{
		{"docs/**/*.md", "docs/a/b/c.md", true},
		{"docs/**/*.md", "docs/c.md", true},
		{"docs/**/*.md", "other/c.md", false},
		{"docs/**.md", "docs/a/b/c.md", true}, // historical advertised form
		{"docs/**.md", "docs/c.md", true},
		{"**/CHANGELOG.md", "deep/nested/CHANGELOG.md", true},
		{"**/CHANGELOG.md", "CHANGELOG.md", true},
		{"*.md", "docs/c.md", false}, // single * does not cross /
		{"node_modules/**", "node_modules/x/y.md", true},
	}
	for _, tt := range tests {
		if got := matchDocsGlob(tt.pattern, tt.path); got != tt.want {
			t.Errorf("matchDocsGlob(%q, %q) = %v, want %v", tt.pattern, tt.path, got, tt.want)
		}
	}
}

func TestDocsFlattenGlobAnyBasename(t *testing.T) {
	// Patterns without "/" keep matching by basename (legacy behavior).
	if !docsFlattenGlobAny("deep/nested/README.md", []string{"README.md"}) {
		t.Error("basename pattern should match nested file")
	}
	if docsFlattenGlobAny("deep/nested/OTHER.md", []string{"README.md"}) {
		t.Error("basename pattern must not match different file")
	}
}

func TestSanitizeMDX(t *testing.T) {
	doc := strings.Join([]string{
		`import { Card } from '@mintlify/components'`,
		`export const x = 1`,
		``,
		`# Title`,
		``,
		`<Card title="Setup" icon="rocket">`,
		`Install the CLI first.`,
		`</Card>`,
		``,
		`<Snippet file="warning.mdx" />`,
		``,
		`<Tab`,
		`  title="macOS"`,
		`>`,
		`brew install chatcli`,
		"```sh",
		`# fenced comment stays`,
		`<NotAComponent> stays in fence`,
		"```",
		`a < b stays as prose`,
	}, "\n")

	got := sanitizeMDX(doc)

	for _, banned := range []string{"import {", "export const", "<Card", "</Card>", "<Snippet", "<Tab"} {
		if strings.Contains(got, banned) {
			t.Errorf("sanitized MDX still contains %q:\n%s", banned, got)
		}
	}
	for _, kept := range []string{"# Title", "Install the CLI first.", "brew install chatcli", "# fenced comment stays", "<NotAComponent> stays in fence", "a < b stays as prose"} {
		if !strings.Contains(got, kept) {
			t.Errorf("sanitized MDX lost %q:\n%s", kept, got)
		}
	}
}

func TestParseDocsFlattenArgs(t *testing.T) {
	t.Run("flat json", func(t *testing.T) {
		cfg, err := parseDocsFlattenArgs([]string{`{"root":"./docs","format":"jsonl","maxChars":500,"stripFrontMatter":false,"output":"/tmp/x.jsonl"}`})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Root != "./docs" || cfg.Format != "jsonl" || cfg.MaxChars != 500 || cfg.StripFrontMatter || cfg.Output != "/tmp/x.jsonl" {
			t.Errorf("unexpected cfg: %+v", cfg)
		}
	})
	t.Run("envelope", func(t *testing.T) {
		cfg, err := parseDocsFlattenArgs([]string{`{"cmd":"flatten","args":{"repo":"https://github.com/org/x","subdir":"docs"}}`})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Repo != "https://github.com/org/x" || cfg.Subdir != "docs" || cfg.Branch != "main" {
			t.Errorf("unexpected cfg: %+v", cfg)
		}
	})
	t.Run("argv flags", func(t *testing.T) {
		cfg, err := parseDocsFlattenArgs([]string{"--root", "./docs", "--format", "yaml", "--include", "a.md,b/**/*.md"})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Root != "./docs" || cfg.Format != "yaml" || len(cfg.Include) != 2 {
			t.Errorf("unexpected cfg: %+v", cfg)
		}
	})
	t.Run("missing source", func(t *testing.T) {
		if _, err := parseDocsFlattenArgs([]string{`{"format":"text"}`}); err == nil {
			t.Error("expected error when neither root nor repo given")
		}
	})
	t.Run("both sources", func(t *testing.T) {
		if _, err := parseDocsFlattenArgs([]string{`{"root":".","repo":"https://x/y"}`}); err == nil {
			t.Error("expected error when root and repo both given")
		}
	})
	t.Run("bad format", func(t *testing.T) {
		if _, err := parseDocsFlattenArgs([]string{`{"root":".","format":"xml"}`}); err == nil {
			t.Error("expected error on invalid format")
		}
	})
	t.Run("flag-like repo rejected", func(t *testing.T) {
		if _, err := parseDocsFlattenArgs([]string{`{"repo":"--upload-pack=evil"}`}); err == nil {
			t.Error("expected error on flag-like repo URL")
		}
	})
	t.Run("invalid branch rejected", func(t *testing.T) {
		if _, err := parseDocsFlattenArgs([]string{`{"repo":"https://github.com/org/x","branch":"-evil"}`}); err == nil {
			t.Error("expected error on flag-like branch")
		}
		for branch, want := range map[string]bool{
			"main": true, "release/1.2": true, "v1.2.3": true,
			"-evil": false, "has space": false, "ctl\tchar": false, "": false,
		} {
			if got := isValidGitBranch(branch); got != want {
				t.Errorf("isValidGitBranch(%q) = %v, want %v", branch, got, want)
			}
		}
	})
}

// writeDocsTree builds a small docs corpus for end-to-end tests.
func writeDocsTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"index.md":        "---\ntitle: Home\n---\nWelcome to the docs.",
		"guide/setup.md":  "# Setup\nInstall it.",
		"guide/usage.mdx": "---\ntitle: Usage\n---\nimport X from 'x'\n\n<Card>\nRun the tool.\n</Card>",
		"skip/notes.txt":  "not markdown",
		".hidden/h.md":    "# Hidden\nshould be skipped",
	}
	for rel, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestDocsFlattenEndToEndJSONL(t *testing.T) {
	dir := writeDocsTree(t)
	p := NewBuiltinDocsFlattenPlugin()
	out, err := p.Execute(context.Background(), []string{`{"root":"` + filepath.ToSlash(dir) + `","format":"jsonl"}`})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 chunks (index, setup, usage), got %d:\n%s", len(lines), out)
	}
	sources := map[string]bool{}
	for _, line := range lines {
		var c docsFlattenChunk
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			t.Fatalf("invalid JSONL line %q: %v", line, err)
		}
		if c.ID == "" || c.Source == "" || c.Content == "" || c.ChunkSize != len(c.Content) {
			t.Errorf("malformed chunk: %+v", c)
		}
		sources[c.Source] = true
	}
	if sources[".hidden/h.md"] || len(sources) != 3 {
		t.Errorf("unexpected sources: %v", sources)
	}
	// Front-matter title became a heading; MDX plumbing is gone.
	if !strings.Contains(out, "# Home") {
		t.Error("stripFrontMatter should re-emit the title as heading")
	}
	if strings.Contains(out, "<Card>") || strings.Contains(out, "import X") {
		t.Error("MDX components/imports must be stripped from .mdx chunks")
	}
}

func TestDocsFlattenEndToEndOutputFile(t *testing.T) {
	dir := writeDocsTree(t)
	outPath := filepath.Join(t.TempDir(), "sub", "corpus.jsonl")
	p := NewBuiltinDocsFlattenPlugin()
	summary, err := p.Execute(context.Background(), []string{`{"root":"` + filepath.ToSlash(dir) + `","format":"jsonl","output":"` + filepath.ToSlash(outPath) + `"}`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, "3 chunks") || !strings.Contains(summary, "--mode knowledge") {
		t.Errorf("summary missing expected hints: %q", summary)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("output file not written: %v", err)
	}
	if len(strings.Split(strings.TrimSpace(string(data)), "\n")) != 3 {
		t.Error("output file should hold the 3 JSONL chunks")
	}
}

func TestDocsFlattenIncludeExclude(t *testing.T) {
	dir := writeDocsTree(t)
	p := NewBuiltinDocsFlattenPlugin()
	out, err := p.Execute(context.Background(), []string{`{"root":"` + filepath.ToSlash(dir) + `","format":"jsonl","include":"guide/**","exclude":"**/*.mdx"}`})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], "guide/setup.md") {
		t.Errorf("include/exclude filtering failed:\n%s", out)
	}
}

func TestDocsFlattenTextAndYAMLFormats(t *testing.T) {
	dir := writeDocsTree(t)
	p := NewBuiltinDocsFlattenPlugin()

	text, err := p.Execute(context.Background(), []string{`{"root":"` + filepath.ToSlash(dir) + `","format":"text","include":"index.md"}`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "===== FILE: index.md =====") || !strings.Contains(text, "TITLE: Home") {
		t.Errorf("text format banners missing:\n%s", text)
	}

	yamlOut, err := p.Execute(context.Background(), []string{`{"root":"` + filepath.ToSlash(dir) + `","format":"yaml","include":"index.md"}`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(yamlOut, "source: index.md") || !strings.Contains(yamlOut, "chunkSize:") {
		t.Errorf("yaml format fields missing:\n%s", yamlOut)
	}
}

func TestDocsFlattenCaps(t *testing.T) {
	p := NewBuiltinDocsFlattenPlugin()
	localInline := []string{`{"root":"./docs"}`}
	withOutput := []string{`{"root":"./docs","output":"/tmp/x.jsonl"}`}
	withRepo := []string{`{"repo":"https://github.com/org/x"}`}

	if !p.IsReadOnly(localInline) || !p.IsConcurrencySafe(localInline) {
		t.Error("local inline flatten should be read-only and concurrency-safe")
	}
	if p.IsReadOnly(withOutput) {
		t.Error("writing an output file is not read-only")
	}
	if p.IsReadOnly(withRepo) {
		t.Error("cloning a repo is not read-only")
	}
	if got := p.DescribeCall(localInline); !strings.Contains(got, "./docs") {
		t.Errorf("DescribeCall should surface the root: %q", got)
	}
}
