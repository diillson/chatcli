/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * BuiltinDocsFlattenPlugin — @docs-flatten as a native ReAct tool.
 *
 * Flattens a Markdown documentation tree (a local directory or a shallow git
 * clone) into AI-ready chunks rendered as text, jsonl, json or yaml. The JSONL
 * output uses the exact schema cli/ctxmgr ingests for /context --mode
 * knowledge, so the agent can build a knowledge base end-to-end without any
 * external plugin (and therefore without plugin signing).
 *
 * Ported from plugins-examples/chatcli-docs-flatten (which remains as a
 * plugin-authoring example) with fixes the external version lacked: real **
 * glob support, heading-aware chunking that never splits inside code fences,
 * unquoted front-matter titles, .mdx/.markdown extensions and spec-correct
 * YAML output via yaml.v3.
 */
package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/diillson/chatcli/i18n"
	"gopkg.in/yaml.v3"
)

// docsFlattenChunk is one unit of flattened documentation. The JSON field
// names are a wire contract: cli/ctxmgr's knowledge ingestion parses exactly
// this shape (id/source/title/content/chunkSize/repoUrl/commit) — renaming a
// field silently breaks /context --mode knowledge corpora.
type docsFlattenChunk struct {
	ID        string `json:"id" yaml:"id"`
	Source    string `json:"source" yaml:"source"`
	Title     string `json:"title,omitempty" yaml:"title,omitempty"`
	Content   string `json:"content" yaml:"content"`
	ChunkSize int    `json:"chunkSize" yaml:"chunkSize"`
	RepoURL   string `json:"repoUrl,omitempty" yaml:"repoUrl,omitempty"`
	Commit    string `json:"commit,omitempty" yaml:"commit,omitempty"`
}

// docsFlattenArgs is the typed view of @docs-flatten's JSON input.
type docsFlattenArgs struct {
	Root             string
	Repo             string
	URL              string
	MaxPages         int
	MaxDepth         int
	SameHost         bool
	Branch           string
	Subdir           string
	Format           string
	MaxChars         int
	Include          []string
	Exclude          []string
	StripFrontMatter bool
	Output           string
}

// docsFlattenExts are the Markdown extensions the walker accepts.
var docsFlattenExts = map[string]bool{".md": true, ".mdx": true, ".markdown": true}

// Front-matter title extractors. Quoted and unquoted values are both
// accepted — Hugo/Docusaurus corpora commonly use `title: Getting Started`
// without quotes.
var (
	docsFlattenTitleTOML = regexp.MustCompile(`(?i)^\s*title\s*=\s*(.+?)\s*$`)
	docsFlattenTitleYAML = regexp.MustCompile(`(?i)^\s*title\s*:\s*(.+?)\s*$`)
	docsFlattenHeading   = regexp.MustCompile(`^#{1,6}\s`)
)

// BuiltinDocsFlattenPlugin is the @docs-flatten tool.
type BuiltinDocsFlattenPlugin struct{}

// NewBuiltinDocsFlattenPlugin returns a ready-to-register plugin.
func NewBuiltinDocsFlattenPlugin() *BuiltinDocsFlattenPlugin { return &BuiltinDocsFlattenPlugin{} }

// Name returns "@docs-flatten".
func (*BuiltinDocsFlattenPlugin) Name() string { return "@docs-flatten" }

// Description surfaces the tool in the catalog.
func (*BuiltinDocsFlattenPlugin) Description() string {
	return i18n.T("plugins.docsflatten.description")
}

// Usage explains the canonical invocation.
func (*BuiltinDocsFlattenPlugin) Usage() string {
	return `<tool_call name="@docs-flatten" args='{"root":"./docs","format":"jsonl","output":"/tmp/corpus.jsonl"}' />

Flags (flat JSON or {"cmd":"flatten","args":{...}} envelope):
  root      local directory with Markdown docs (one source: root|repo|url)
  repo      git URL to shallow-clone (one source: root|repo|url)
  url       seed URL of a rendered docs WEBSITE to crawl (one source:
            root|repo|url). Flattens HTML pages — no Markdown repo needed.
  maxPages  max pages to crawl when using url (default: 50)
  maxDepth  max BFS link depth from the seed when using url (default: 2)
  sameHost  only follow links on the seed's host when crawling (default: true)
  branch    branch to clone (default: main)
  subdir    docs subdirectory inside the repo (e.g. "docs")
  format    text | jsonl | json | yaml (default: text; use jsonl for
            /context --mode knowledge corpora)
  maxChars  max characters per chunk, 0 = no splitting (default: 16000)
  include   comma-separated globs to include (** supported, e.g. docs/**/*.md)
  exclude   comma-separated globs to exclude
  stripFrontMatter  remove front matter, keep title as heading (default: true)
  output    file to write the result to. STRONGLY preferred for large corpora
            — without it the flattened content is returned (and truncated)
            in the tool result.`
}

// Version is semver. 2.x marks the builtin port of the 1.x external example.
func (*BuiltinDocsFlattenPlugin) Version() string { return "2.0.0" }

// Path is empty for builtin plugins.
func (*BuiltinDocsFlattenPlugin) Path() string { return "" }

// Schema describes the tool for the LLM catalog.
func (*BuiltinDocsFlattenPlugin) Schema() string {
	schema := map[string]interface{}{
		"argsFormat": "flat JSON preferred",
		"subcommands": []map[string]interface{}{
			{
				"name": "flatten",
				"description": "Flatten documentation into AI-ready chunks from one of three sources: a local Markdown/MDX tree (root), a git repo to shallow-clone (repo), or a rendered docs WEBSITE crawled over HTTP (url — no Markdown repo required). " +
					"Handles .md, .mdx (Mintlify/Docusaurus — JSX components and imports are stripped, prose kept) and .markdown for root/repo; for url it crawls HTML pages, strips tags to clean text and chunks the same way. " +
					"Use format=jsonl + output=<file> to produce a corpus for /context create <name> <file> --mode knowledge. " +
					"ALWAYS set output when the corpus may be large — inline results are truncated.",
				"flags": []map[string]interface{}{
					{"name": "root", "type": "string", "description": "Local directory with the Markdown docs. Exactly one of root|repo|url."},
					{"name": "repo", "type": "string", "description": "Git URL (https://... or git@host:path). Exactly one of root|repo|url."},
					{"name": "url", "type": "string", "description": "Seed URL of a rendered docs website (http/https) to crawl and flatten into the same JSONL corpus. Exactly one of root|repo|url."},
					{"name": "maxPages", "type": "integer", "description": "Max pages to crawl when using url. Default: 50."},
					{"name": "maxDepth", "type": "integer", "description": "Max BFS link depth from the seed when using url. Default: 2."},
					{"name": "sameHost", "type": "boolean", "description": "When crawling url, only follow links on the seed's host. Default: true."},
					{"name": "branch", "type": "string", "description": "Branch to clone (only with repo). Default: main."},
					{"name": "subdir", "type": "string", "description": "Docs subdirectory inside the repo (only with repo), e.g. 'docs'."},
					{"name": "format", "type": "string", "description": "text | jsonl | json | yaml. Default: text. Use jsonl for knowledge corpora."},
					{"name": "maxChars", "type": "integer", "description": "Max characters per chunk (0 = no split). Default: 16000."},
					{"name": "include", "type": "string", "description": "Comma-separated include globs, ** crosses directories (e.g. 'docs/**/*.md'). Bare names match by basename."},
					{"name": "exclude", "type": "string", "description": "Comma-separated exclude globs (e.g. 'node_modules/**,CHANGELOG.md')."},
					{"name": "stripFrontMatter", "type": "boolean", "description": "Strip YAML/TOML front matter, re-emitting its title as a heading. Default: true."},
					{"name": "output", "type": "string", "description": "File path to write the result to. Required in practice for large corpora."},
				},
				"examples": []string{
					`{"root":"./docs","format":"jsonl","output":"/tmp/corpus.jsonl"}`,
					`{"repo":"https://github.com/org/project","subdir":"docs","format":"jsonl","output":"/tmp/corpus.jsonl"}`,
					`{"url":"https://docs.example.com/","maxPages":40,"maxDepth":2,"format":"jsonl","output":"/tmp/corpus.jsonl"}`,
					`{"root":".","include":"README.md"}`,
				},
			},
		},
	}
	data, _ := json.Marshal(schema)
	return string(data)
}

// Execute parses args and dispatches.
func (p *BuiltinDocsFlattenPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream runs the flatten pipeline, streaming per-file progress.
func (p *BuiltinDocsFlattenPlugin) ExecuteWithStream(ctx context.Context, args []string, onOutput func(string)) (string, error) {
	cfg, err := parseDocsFlattenArgs(args)
	if err != nil {
		return "", fmt.Errorf("@docs-flatten: %w", err)
	}
	emit := func(line string) {
		if onOutput != nil {
			onOutput(line)
		}
	}

	if cfg.URL != "" {
		return p.executeURL(ctx, cfg, emit)
	}

	root, provenance, cleanup, err := resolveDocsFlattenRoot(ctx, cfg, emit)
	if err != nil {
		return "", fmt.Errorf("@docs-flatten: %w", err)
	}
	defer cleanup()

	chunks, files, err := walkDocsFlatten(ctx, root, cfg, provenance, emit)
	if err != nil {
		return "", fmt.Errorf("@docs-flatten: %w", err)
	}
	if len(chunks) == 0 {
		return fmt.Sprintf("@docs-flatten: no Markdown files matched under %s", root), nil
	}

	rendered, err := renderDocsFlatten(chunks, cfg.Format)
	if err != nil {
		return "", fmt.Errorf("@docs-flatten: %w", err)
	}

	if cfg.Output == "" {
		return rendered, nil
	}
	if err := writeDocsFlattenOutput(cfg.Output, rendered); err != nil {
		return "", fmt.Errorf("@docs-flatten: %w", err)
	}
	summary := fmt.Sprintf("@docs-flatten: %d chunks from %d files → %s (format=%s, %s)",
		len(chunks), files, cfg.Output, cfg.Format, humanByteSize(len(rendered)))
	if provenance.RepoURL != "" {
		summary += fmt.Sprintf("\nsource: %s @ %s", provenance.RepoURL, provenance.Commit)
	}
	if cfg.Format == "jsonl" {
		summary += fmt.Sprintf("\nready for: /context create <name> %s --mode knowledge", cfg.Output)
	}
	return summary, nil
}

// docsFlattenProvenance records where a cloned corpus came from.
type docsFlattenProvenance struct {
	RepoURL string
	Commit  string
}

// parseDocsFlattenArgs supports flat JSON, the {"cmd","args"} envelope and
// --flag argv form.
func parseDocsFlattenArgs(args []string) (docsFlattenArgs, error) {
	out := docsFlattenArgs{Branch: "main", Format: "text", MaxChars: 16000, StripFrontMatter: true, MaxPages: 50, MaxDepth: 2, SameHost: true}
	payload := strings.TrimSpace(strings.Join(args, " "))
	var raw map[string]json.RawMessage
	switch {
	case strings.HasPrefix(payload, "{"):
		if err := json.Unmarshal([]byte(payload), &raw); err != nil {
			return out, fmt.Errorf("malformed JSON args: %w", err)
		}
		if inner, ok := raw["args"]; ok {
			var innerMap map[string]json.RawMessage
			if err := json.Unmarshal(inner, &innerMap); err == nil {
				raw = innerMap
			}
		}
	default:
		var jsonErr error
		raw, jsonErr = docsFlattenArgvToMap(args)
		if jsonErr != nil {
			return out, jsonErr
		}
	}

	out.Root = jsonString(raw, "root", "dir", "path")
	out.Repo = jsonString(raw, "repo", "repoUrl")
	parseDocsFlattenCrawlArgs(raw, &out)
	if v := jsonString(raw, "branch"); v != "" {
		out.Branch = v
	}
	out.Subdir = jsonString(raw, "subdir")
	if v := strings.ToLower(jsonString(raw, "format")); v != "" {
		out.Format = v
	}
	if v, ok := raw["maxChars"]; ok && len(v) > 0 {
		out.MaxChars = jsonInt(raw, "maxChars")
	} else if _, ok := raw["max-chars"]; ok {
		out.MaxChars = jsonInt(raw, "max-chars")
	}
	out.Include = splitDocsFlattenCSV(jsonString(raw, "include"))
	out.Exclude = splitDocsFlattenCSV(jsonString(raw, "exclude"))
	if v, present := jsonBoolLookup(raw, "stripFrontMatter", "strip-front-matter"); present {
		out.StripFrontMatter = v
	}
	out.Output = jsonString(raw, "output", "out")

	if err := validateDocsFlattenSource(&out); err != nil {
		return out, err
	}
	switch out.Format {
	case "text", "jsonl", "json", "yaml":
	default:
		return out, fmt.Errorf("invalid format %q (valid: text|jsonl|json|yaml)", out.Format)
	}
	if out.Repo != "" && !isLikelyGitURL(out.Repo) {
		return out, fmt.Errorf("repo %q does not look like a git URL (expected https://…, ssh://… or git@host:path)", out.Repo)
	}
	if !isValidGitBranch(out.Branch) {
		return out, fmt.Errorf("invalid branch name %q", out.Branch)
	}
	return out, nil
}

// parseDocsFlattenCrawlArgs extracts the web-crawl source ("url") and its
// bounds (maxPages/maxDepth/sameHost), supporting both camelCase and
// kebab-case keys.
func parseDocsFlattenCrawlArgs(raw map[string]json.RawMessage, out *docsFlattenArgs) {
	out.URL = jsonString(raw, "url", "seed")
	if v, ok := raw["maxPages"]; ok && len(v) > 0 {
		out.MaxPages = jsonInt(raw, "maxPages")
	} else if _, ok := raw["max-pages"]; ok {
		out.MaxPages = jsonInt(raw, "max-pages")
	}
	if v, ok := raw["maxDepth"]; ok && len(v) > 0 {
		out.MaxDepth = jsonInt(raw, "maxDepth")
	} else if _, ok := raw["max-depth"]; ok {
		out.MaxDepth = jsonInt(raw, "max-depth")
	}
	if v, present := jsonBoolLookup(raw, "sameHost", "same-host"); present {
		out.SameHost = v
	}
}

// validateDocsFlattenSource enforces that exactly one of root|repo|url is set
// and sanity-checks the url source (scheme + non-negative bounds).
func validateDocsFlattenSource(out *docsFlattenArgs) error {
	sources := 0
	for _, set := range []bool{out.Root != "", out.Repo != "", out.URL != ""} {
		if set {
			sources++
		}
	}
	if sources == 0 {
		return errors.New(`one of "root", "repo" or "url" is required`)
	}
	if sources > 1 {
		return errors.New(`"root", "repo" and "url" are mutually exclusive — set exactly one`)
	}
	if out.URL == "" {
		return nil
	}
	if _, err := validateWebTarget(out.URL); err != nil {
		return fmt.Errorf("invalid url %q: %w", out.URL, err)
	}
	if out.MaxPages <= 0 {
		out.MaxPages = 50
	}
	if out.MaxDepth < 0 {
		out.MaxDepth = 0
	}
	return nil
}

// isValidGitBranch enforces the subset of git-check-ref-format rules that
// matters before handing the value to a subprocess: no flag-like leading
// dash, no whitespace or control characters.
func isValidGitBranch(branch string) bool {
	if branch == "" || strings.HasPrefix(branch, "-") {
		return false
	}
	for _, r := range branch {
		if r <= ' ' || r == 0x7f {
			return false
		}
	}
	return true
}

// docsFlattenArgvToMap converts `--flag value` argv form into the same raw
// map the JSON path produces, so both forms share one extraction site.
func docsFlattenArgvToMap(args []string) (map[string]json.RawMessage, error) {
	raw := make(map[string]json.RawMessage)
	for i := 0; i < len(args); i++ {
		a := strings.TrimSpace(args[i])
		if a == "" {
			continue
		}
		if !strings.HasPrefix(a, "--") {
			// Bare positional value: treat as root.
			if _, ok := raw["root"]; !ok {
				v, _ := json.Marshal(trimQuotes(a))
				raw["root"] = v
			}
			continue
		}
		key := strings.TrimPrefix(a, "--")
		val := ""
		if eq := strings.Index(key, "="); eq >= 0 {
			val = key[eq+1:]
			key = key[:eq]
		} else if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
			val = args[i+1]
			i++
		}
		v, _ := json.Marshal(trimQuotes(val))
		raw[key] = v
	}
	if len(raw) == 0 {
		return nil, errors.New(`either "root" or "repo" is required`)
	}
	return raw, nil
}

// jsonBoolLookup reads a boolean (or stringified boolean) from any of the
// aliased keys, reporting whether the key was present at all so callers can
// keep a true default.
func jsonBoolLookup(raw map[string]json.RawMessage, keys ...string) (value, present bool) {
	for _, k := range keys {
		v, ok := raw[k]
		if !ok {
			continue
		}
		var b bool
		if err := json.Unmarshal(v, &b); err == nil {
			return b, true
		}
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			if parsed, perr := strconv.ParseBool(strings.TrimSpace(s)); perr == nil {
				return parsed, true
			}
		}
	}
	return false, false
}

func splitDocsFlattenCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = trimQuotes(strings.TrimSpace(p))
		if p != "" {
			out = append(out, filepath.ToSlash(p))
		}
	}
	return out
}

// isLikelyGitURL gates what we hand to `git clone`. Rejecting flag-like and
// local-path-like values keeps the subprocess invocation unambiguous.
func isLikelyGitURL(s string) bool {
	if strings.HasPrefix(s, "-") {
		return false
	}
	for _, prefix := range []string{"https://", "http://", "ssh://", "git://"} {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	// scp-like: git@host:path
	if strings.HasPrefix(s, "git@") && strings.Contains(s, ":") {
		return true
	}
	return false
}

// resolveDocsFlattenRoot returns the directory to walk: the local root as-is,
// or a fresh shallow clone (always removed by the returned cleanup).
func resolveDocsFlattenRoot(ctx context.Context, cfg docsFlattenArgs, emit func(string)) (string, docsFlattenProvenance, func(), error) {
	noop := func() {}
	if cfg.Repo == "" {
		abs, err := filepath.Abs(cfg.Root)
		if err != nil {
			return "", docsFlattenProvenance{}, noop, err
		}
		info, err := os.Stat(abs)
		if err != nil || !info.IsDir() {
			return "", docsFlattenProvenance{}, noop, fmt.Errorf("root is not a directory: %s", cfg.Root)
		}
		return abs, docsFlattenProvenance{}, noop, nil
	}

	tmpDir, err := os.MkdirTemp("", "docs-flatten-*")
	if err != nil {
		return "", docsFlattenProvenance{}, noop, fmt.Errorf("create temp dir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(tmpDir) }

	emit(fmt.Sprintf("cloning %s (branch=%s)…", cfg.Repo, cfg.Branch))
	var stderr bytes.Buffer
	// "--" terminates flag parsing so a hostile URL/branch can't smuggle git flags.
	cmd := exec.CommandContext(ctx, "git", "clone", "--depth", "1", "--branch", cfg.Branch, "--", cfg.Repo, tmpDir) //#nosec G204 -- repo validated by isLikelyGitURL, branch by isValidGitBranch, "--" terminates flag parsing
	cmd.Stdout = nil
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		cleanup()
		return "", docsFlattenProvenance{}, noop, fmt.Errorf("git clone failed: %w\n%s", err, strings.TrimSpace(stderr.String()))
	}

	prov := docsFlattenProvenance{RepoURL: cfg.Repo}
	if out, err := exec.CommandContext(ctx, "git", "-C", tmpDir, "rev-parse", "HEAD").Output(); err == nil { //#nosec G204 -- tmpDir comes from os.MkdirTemp, not user input
		prov.Commit = strings.TrimSpace(string(out))
	}

	root := tmpDir
	if cfg.Subdir != "" {
		root = filepath.Join(tmpDir, cfg.Subdir)
		if info, err := os.Stat(root); err != nil || !info.IsDir() {
			cleanup()
			return "", docsFlattenProvenance{}, noop, fmt.Errorf("subdir %q not found in repository", cfg.Subdir)
		}
	}
	return root, prov, cleanup, nil
}

// walkDocsFlatten walks root and converts every matching Markdown file into
// chunks. Returns the chunks and the number of contributing files.
func walkDocsFlatten(ctx context.Context, root string, cfg docsFlattenArgs, prov docsFlattenProvenance, emit func(string)) ([]docsFlattenChunk, int, error) {
	var chunks []docsFlattenChunk
	files := 0
	chunkIndex := 1

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // unreadable entries are skipped, not fatal
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if !docsFlattenExts[strings.ToLower(filepath.Ext(d.Name()))] {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel)
		if !docsFlattenSelected(rel, cfg) {
			return nil
		}

		fileChunks, err := docsFlattenFile(path, rel, cfg, &chunkIndex, prov)
		if err != nil {
			emit(fmt.Sprintf("skipping %s: %v", rel, err))
			return nil
		}
		if len(fileChunks) > 0 {
			files++
			chunks = append(chunks, fileChunks...)
			emit(fmt.Sprintf("processed %s → %d chunk(s)", rel, len(fileChunks)))
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return chunks, files, nil
}

func docsFlattenSelected(rel string, cfg docsFlattenArgs) bool {
	if len(cfg.Include) > 0 && !docsFlattenGlobAny(rel, cfg.Include) {
		return false
	}
	if len(cfg.Exclude) > 0 && docsFlattenGlobAny(rel, cfg.Exclude) {
		return false
	}
	return true
}

// docsFlattenFile reads one Markdown file and converts it into chunks.
func docsFlattenFile(absPath, relPath string, cfg docsFlattenArgs, chunkIndex *int, prov docsFlattenProvenance) ([]docsFlattenChunk, error) {
	data, err := os.ReadFile(absPath) // #nosec G304 -- path comes from the walked docs tree the user pointed the tool at
	if err != nil {
		return nil, err
	}

	title, body, hasFM := parseDocsFlattenFrontMatter(string(data))
	content := string(data)
	if cfg.StripFrontMatter {
		content = body
		if hasFM && title != "" {
			content = "# " + title + "\n\n" + body
		}
	}
	if strings.EqualFold(filepath.Ext(relPath), ".mdx") {
		content = sanitizeMDX(content)
	}
	content = normalizeDocsFlattenMarkdown(content)
	if strings.TrimSpace(content) == "" {
		return nil, nil
	}

	raw := chunkMarkdown(content, cfg.MaxChars)
	chunks := make([]docsFlattenChunk, 0, len(raw))
	for _, c := range raw {
		chunks = append(chunks, docsFlattenChunk{
			ID:        fmt.Sprintf("%s#%04d", relPath, *chunkIndex),
			Source:    relPath,
			Title:     title,
			Content:   c,
			ChunkSize: len(c),
			RepoURL:   prov.RepoURL,
			Commit:    prov.Commit,
		})
		*chunkIndex++
	}
	return chunks, nil
}

// parseDocsFlattenFrontMatter extracts the title from a YAML (---) or TOML
// (+++) front-matter block and returns the body without it. Quoted and
// unquoted titles are both supported.
func parseDocsFlattenFrontMatter(data string) (title, body string, hasFM bool) {
	lines := strings.Split(data, "\n")
	if len(lines) == 0 {
		return "", data, false
	}
	fence := strings.TrimSpace(lines[0])
	var titleRe *regexp.Regexp
	switch fence {
	case "---":
		titleRe = docsFlattenTitleYAML
	case "+++":
		titleRe = docsFlattenTitleTOML
	default:
		return "", data, false
	}

	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == fence {
			end = i
			break
		}
	}
	if end == -1 {
		return "", data, false
	}

	for _, line := range lines[1:end] {
		if m := titleRe.FindStringSubmatch(line); len(m) == 2 {
			title = trimQuotes(strings.TrimSpace(m[1]))
			break
		}
	}
	return title, strings.Join(lines[end+1:], "\n"), true
}

// normalizeDocsFlattenMarkdown unifies line endings and collapses runs of
// blank lines to a single one.
func normalizeDocsFlattenMarkdown(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// chunkMarkdown splits content into chunks of at most maxChars characters,
// preferring section boundaries (ATX headings outside code fences) so a
// retrieval hit never starts mid-section. Oversized sections fall back to
// line-boundary packing. maxChars <= 0 disables splitting.
func chunkMarkdown(text string, maxChars int) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if maxChars <= 0 || len(text) <= maxChars {
		return []string{text}
	}

	var chunks []string
	var buf strings.Builder
	flush := func() {
		if buf.Len() > 0 {
			chunks = append(chunks, buf.String())
			buf.Reset()
		}
	}
	for _, sec := range splitMarkdownSections(text) {
		if len(sec) > maxChars {
			flush()
			chunks = append(chunks, chunkLines(sec, maxChars)...)
			continue
		}
		if buf.Len() > 0 && buf.Len()+len(sec)+1 > maxChars {
			flush()
		}
		if buf.Len() > 0 {
			buf.WriteByte('\n')
		}
		buf.WriteString(sec)
	}
	flush()
	return chunks
}

// splitMarkdownSections splits at ATX headings, tracking ``` / ~~~ fences so
// a "# comment" line inside a shell snippet never opens a new section.
func splitMarkdownSections(text string) []string {
	lines := strings.Split(text, "\n")
	var sections []string
	start := 0
	inFence := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
		}
		if !inFence && docsFlattenHeading.MatchString(trimmed) && i > start {
			sections = append(sections, strings.Join(lines[start:i], "\n"))
			start = i
		}
	}
	return append(sections, strings.Join(lines[start:], "\n"))
}

// chunkLines is the line-boundary fallback packer for oversized sections.
func chunkLines(text string, maxChars int) []string {
	var chunks []string
	var buf strings.Builder
	for _, line := range strings.Split(text, "\n") {
		if buf.Len() > 0 && buf.Len()+len(line)+1 > maxChars {
			chunks = append(chunks, buf.String())
			buf.Reset()
		}
		if buf.Len() > 0 {
			buf.WriteByte('\n')
		}
		buf.WriteString(line)
	}
	if buf.Len() > 0 {
		chunks = append(chunks, buf.String())
	}
	return chunks
}

// JSX component tags in MDX prose: <Card title="…">, </Tabs>, <Snippet … />.
// Component names are capitalized by convention, which keeps plain HTML-ish
// usage (<br>, <img>) and comparison prose ("a < b") untouched.
var mdxComponentTag = regexp.MustCompile(`</?[A-Z][A-Za-z0-9.]*(\s[^<>]*)?/?>`)

// sanitizeMDX strips the MDX layer (Mintlify, Docusaurus MDX pages) so chunks
// carry prose, not JSX plumbing: top-level import/export statements go away
// and component tags are removed while their inner text is preserved. Fenced
// code blocks pass through untouched. Multi-line component tags (attributes
// spread across lines, as Mintlify emits) are joined before stripping.
func sanitizeMDX(content string) string {
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	inFence := false
	var pendingTag []string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			out = append(out, line)
			continue
		}
		if inFence {
			out = append(out, line)
			continue
		}

		// Continuation of a component tag opened on a previous line.
		if len(pendingTag) > 0 {
			pendingTag = append(pendingTag, line)
			if strings.Contains(line, ">") {
				joined := strings.Join(pendingTag, " ")
				out = append(out, mdxComponentTag.ReplaceAllString(joined, ""))
				pendingTag = nil
			}
			continue
		}

		if strings.HasPrefix(trimmed, "import ") || strings.HasPrefix(trimmed, "export ") {
			continue
		}

		// A component tag that opens but doesn't close on this line.
		if mdxOpensUnclosedTag(trimmed) {
			pendingTag = append(pendingTag, line)
			continue
		}

		out = append(out, mdxComponentTag.ReplaceAllString(line, ""))
	}
	// Unterminated tag at EOF: emit what we collected rather than dropping it.
	if len(pendingTag) > 0 {
		out = append(out, pendingTag...)
	}
	return strings.Join(out, "\n")
}

// mdxOpensUnclosedTag reports whether the line starts a capitalized JSX
// component tag whose ">" has not arrived yet.
func mdxOpensUnclosedTag(trimmed string) bool {
	i := strings.LastIndex(trimmed, "<")
	if i == -1 || strings.Contains(trimmed[i:], ">") {
		return false
	}
	rest := trimmed[i+1:]
	rest = strings.TrimPrefix(rest, "/")
	return rest != "" && rest[0] >= 'A' && rest[0] <= 'Z'
}

// docsFlattenGlobAny reports whether rel matches any of the patterns.
// Patterns without "/" match against the basename (legacy behavior);
// patterns with "/" match segment-wise with ** crossing directories.
func docsFlattenGlobAny(rel string, patterns []string) bool {
	for _, p := range patterns {
		target := rel
		if !strings.Contains(p, "/") && !strings.Contains(p, "**") {
			target = filepath.Base(rel)
		}
		if matchDocsGlob(p, target) {
			return true
		}
	}
	return false
}

// matchDocsGlob matches a / separated glob against a / separated path. A
// bare "**" segment matches zero or more whole segments; "**X" is normalized
// to "**" + "*X" so the historical advertised form `docs/**.md` keeps
// working. Other segments use filepath.Match semantics.
func matchDocsGlob(pattern, path string) bool {
	parts := strings.Split(pattern, "/")
	segs := make([]string, 0, len(parts)+1)
	for _, s := range parts {
		if s != "**" && strings.Contains(s, "**") {
			segs = append(segs, "**", strings.ReplaceAll(s, "**", "*"))
			continue
		}
		segs = append(segs, s)
	}
	return matchGlobSegments(segs, strings.Split(path, "/"))
}

func matchGlobSegments(pattern, path []string) bool {
	if len(pattern) == 0 {
		return len(path) == 0
	}
	if pattern[0] == "**" {
		for i := 0; i <= len(path); i++ {
			if matchGlobSegments(pattern[1:], path[i:]) {
				return true
			}
		}
		return false
	}
	if len(path) == 0 {
		return false
	}
	ok, err := filepath.Match(pattern[0], path[0])
	if err != nil || !ok {
		return false
	}
	return matchGlobSegments(pattern[1:], path[1:])
}

// renderDocsFlatten serializes chunks in the requested format.
func renderDocsFlatten(chunks []docsFlattenChunk, format string) (string, error) {
	var b bytes.Buffer
	switch format {
	case "text":
		currentSource := ""
		for _, c := range chunks {
			if c.Source != currentSource {
				if currentSource != "" {
					b.WriteByte('\n')
				}
				currentSource = c.Source
				fmt.Fprintf(&b, "===== FILE: %s =====\n", c.Source)
				if c.Title != "" {
					fmt.Fprintf(&b, "TITLE: %s\n", c.Title)
				}
				b.WriteByte('\n')
			}
			b.WriteString(c.Content)
			b.WriteString("\n\n")
		}
	case "jsonl":
		enc := json.NewEncoder(&b)
		for i := range chunks {
			if err := enc.Encode(&chunks[i]); err != nil {
				return "", err
			}
		}
	case "json":
		enc := json.NewEncoder(&b)
		enc.SetIndent("", "  ")
		if err := enc.Encode(chunks); err != nil {
			return "", err
		}
	case "yaml":
		if err := yaml.NewEncoder(&b).Encode(chunks); err != nil {
			return "", err
		}
	default:
		return "", fmt.Errorf("unsupported format %q", format)
	}
	return b.String(), nil
}

func writeDocsFlattenOutput(path, content string) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("create output directory: %w", err)
		}
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return fmt.Errorf("write output file: %w", err)
	}
	return nil
}

// humanByteSize renders a byte count for the summary line.
func humanByteSize(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}
