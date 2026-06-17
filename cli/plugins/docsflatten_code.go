/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Code & infra ingestion for @docs-flatten (kind=code|auto). The Markdown path
 * in builtin_docsflatten.go is unchanged; this file adds the extension gate,
 * skip defaults and structure-aware chunkers so a source / Terraform / GitOps
 * repository flattens into the SAME JSONL chunk schema knowledge mode already
 * ingests. Chunking is heuristic (brace/keyword/document boundaries), not a
 * per-language AST: it targets ~90% structural precision with a universal
 * size-window fallback so a chunk is never unbounded and never lost.
 */
package plugins

import (
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
)

// defaultDocsFlattenMaxFileSize caps per-file ingestion at 1 MiB: anything
// larger is almost always generated, vendored or a data blob, not source a
// knowledge base benefits from.
const defaultDocsFlattenMaxFileSize = 1 << 20

// docsFlattenMaxFileSize resolves the per-file byte cap from config, applying
// the 1 MiB default when unset.
func docsFlattenMaxFileSize(cfg docsFlattenArgs) int64 {
	if cfg.MaxSize > 0 {
		return int64(cfg.MaxSize)
	}
	return defaultDocsFlattenMaxFileSize
}

// docsFlattenCodeCandidates counts files under root that kind=code WOULD ingest
// but the current (docs) run skipped, returning the count and up to a few
// example extensions. It powers the actionable "looks like a code repo —
// re-run with kind=code" hint, so the agent self-corrects in one turn and a
// human gets the same nudge, instead of a dead-end "no Markdown matched". Walks
// at most a bounded number of entries so the probe is cheap on huge trees.
func docsFlattenCodeCandidates(root string) (count int, exts []string) {
	const scanLimit = 5000
	seen := map[string]bool{}
	scanned := 0
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if scanned++; scanned > scanLimit {
			return filepath.SkipAll
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") || docsFlattenSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		ext := fileExt(strings.ToLower(name))
		// Code-eligible but NOT Markdown (which docs mode would already take).
		if markdownExts[ext] || !docsFlattenAcceptExt(name, "code") {
			return nil
		}
		count++
		if label := ext; label != "" && !seen[label] && len(exts) < 4 {
			seen[label] = true
			exts = append(exts, label)
		}
		return nil
	})
	return count, exts
}

// flatChunk is one ready-to-emit piece of a file: its text plus an optional
// human/AI-facing title (a symbol, resource address, or manifest identity).
// docsFlattenFile turns each into a docsFlattenChunk JSONL record.
type flatChunk struct {
	Content string
	Title   string
}

// codeFlavor selects the chunking strategy for a file, derived from its
// extension. flavorMarkdown is handled by the existing Markdown path.
type codeFlavor int

const (
	flavorMarkdown codeFlavor = iota
	flavorBrace               // C-family / Go / JS / TS / Java / … and HCL share {}
	flavorHCL                 // Terraform / HCL — brace blocks with type "name" titles
	flavorYAML                // YAML / Kubernetes / Argo manifests, split on ---
	flavorShell               // shell scripts — function blocks + top-level runs
	flavorIndent              // Python / Ruby — indentation-delimited def/class
	flavorGeneric             // any other text — size-window fallback
)

// Extension → flavor. Lowercase, dot-prefixed. Anything textual not listed
// falls through to flavorGeneric when the kind admits it.
var (
	markdownExts = map[string]bool{".md": true, ".mdx": true, ".markdown": true}

	braceExts = map[string]bool{
		".go": true, ".java": true, ".js": true, ".jsx": true, ".ts": true,
		".tsx": true, ".c": true, ".h": true, ".cpp": true, ".hpp": true,
		".cc": true, ".cs": true, ".kt": true, ".kts": true, ".scala": true,
		".swift": true, ".rs": true, ".php": true, ".dart": true, ".groovy": true,
		".gradle": true, ".proto": true,
	}
	hclExts    = map[string]bool{".tf": true, ".tfvars": true, ".hcl": true}
	yamlExts   = map[string]bool{".yaml": true, ".yml": true}
	shellExts  = map[string]bool{".sh": true, ".bash": true, ".zsh": true}
	indentExts = map[string]bool{".py": true, ".rb": true}

	// genericTextExts are config/data files worth indexing as whole-file
	// windowed text (no structural split that pays off).
	genericTextExts = map[string]bool{
		".json": true, ".toml": true, ".ini": true, ".env": true, ".cfg": true,
		".conf": true, ".properties": true, ".xml": true, ".gitignore": true,
		".dockerfile": true, ".txt": true, ".sql": true, ".tpl": true,
		".gotmpl": true, ".jinja": true, ".j2": true, ".csv": true,
	}
)

// docsFlattenFlavor resolves the chunking flavor for a path. Files with no
// extension but a well-known name (Dockerfile, Makefile) are treated as
// generic text so they still get indexed.
func docsFlattenFlavor(rel string) codeFlavor {
	ext := strings.ToLower(fileExt(rel))
	switch {
	case markdownExts[ext]:
		return flavorMarkdown
	case hclExts[ext]:
		return flavorHCL
	case braceExts[ext]:
		return flavorBrace
	case yamlExts[ext]:
		return flavorYAML
	case shellExts[ext]:
		return flavorShell
	case indentExts[ext]:
		return flavorIndent
	default:
		return flavorGeneric
	}
}

// knownTextlessNames are extensionless files still worth indexing as text.
var knownTextlessNames = map[string]bool{
	"dockerfile": true, "makefile": true, "jenkinsfile": true, "procfile": true,
	"caddyfile": true, "vagrantfile": true, "gemfile": true, "rakefile": true,
	"brewfile": true, ".gitignore": true, ".dockerignore": true, ".env": true,
}

// docsFlattenAcceptExt reports whether a file should be ingested under the given
// kind. docs → Markdown only (legacy). code/auto → Markdown + recognized code,
// config and known textless files; binaries, lockfiles and minified assets are
// always rejected.
func docsFlattenAcceptExt(name, kind string) bool {
	lower := strings.ToLower(name)
	ext := fileExt(lower)

	if kind == "docs" {
		return markdownExts[ext]
	}
	if docsFlattenSkipFile(lower) {
		return false
	}
	if markdownExts[ext] || braceExts[ext] || hclExts[ext] || yamlExts[ext] ||
		shellExts[ext] || indentExts[ext] || genericTextExts[ext] {
		return true
	}
	// Extensionless but well-known (Dockerfile, Makefile, …).
	if ext == "" && knownTextlessNames[lower] {
		return true
	}
	return false
}

// docsFlattenSkipDir reports whether a directory name is build/vendor/VCS noise
// that should never be walked in code/auto mode. Dot-directories are already
// skipped by the walker.
func docsFlattenSkipDir(name string) bool {
	switch strings.ToLower(name) {
	case "vendor", "node_modules", "dist", "build", "target", "out",
		"__pycache__", "bin", "obj", "coverage", "testdata":
		return true
	}
	return false
}

// docsFlattenSkipFile rejects lockfiles, minified assets and binary/asset
// extensions that would only add noise (and tokens) to a knowledge base.
func docsFlattenSkipFile(lowerName string) bool {
	switch lowerName {
	case "go.sum", "package-lock.json", "yarn.lock", "pnpm-lock.yaml",
		"composer.lock", "gemfile.lock", "cargo.lock", "poetry.lock":
		return true
	}
	if strings.HasSuffix(lowerName, ".lock") ||
		strings.HasSuffix(lowerName, ".min.js") ||
		strings.HasSuffix(lowerName, ".min.css") ||
		strings.HasSuffix(lowerName, ".map") {
		return true
	}
	return binaryExts[fileExt(lowerName)]
}

// binaryExts are non-text extensions: indexing them is pure noise.
var binaryExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".svg": true,
	".ico": true, ".webp": true, ".pdf": true, ".zip": true, ".tar": true,
	".gz": true, ".bz2": true, ".xz": true, ".7z": true, ".rar": true,
	".so": true, ".a": true, ".o": true, ".dll": true, ".dylib": true,
	".exe": true, ".bin": true, ".class": true, ".jar": true, ".wasm": true,
	".ttf": true, ".otf": true, ".woff": true, ".woff2": true, ".eot": true,
	".mp3": true, ".mp4": true, ".mov": true, ".avi": true, ".wav": true,
	".db": true, ".sqlite": true, ".pyc": true,
}

// fileExt returns the lowercase extension including the dot ("" if none).
// path/filepath.Ext is avoided to keep this trivially testable on slash paths.
func fileExt(name string) string {
	base := name
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.LastIndexByte(base, '.'); i > 0 {
		return strings.ToLower(base[i:])
	}
	return ""
}

// chunkCode splits a non-Markdown file into titled chunks using the flavor's
// structural unit, then greedily packs units up to maxChars. Oversized single
// units fall back to the line-window packer (chunkLines). Always returns at
// least one chunk for non-empty input.
func chunkCode(flavor codeFlavor, content string, maxChars int) []flatChunk {
	content = normalizeNewlines(content)
	if strings.TrimSpace(content) == "" {
		return nil
	}

	var units []string
	var titleFn func(string) string
	switch flavor {
	case flavorHCL:
		units, titleFn = splitBraceUnits(content), hclTitle
	case flavorBrace, flavorShell:
		// Brace languages and shell both delimit blocks with {}; codeTitle names
		// the unit language-agnostically (symbol, or the signature line).
		units, titleFn = splitBraceUnits(content), codeTitle
	case flavorYAML:
		units, titleFn = splitYAMLDocs(content), yamlTitle
	case flavorIndent:
		units, titleFn = splitIndentUnits(content), codeTitle
	default: // flavorGeneric
		return packGeneric(content, maxChars)
	}

	return emitUnits(units, maxChars, titleFn)
}

// emitUnits turns each structural unit into its own titled chunk — one chunk per
// top-level declaration / resource / manifest — so a retrieval hit and the
// `@knowledge toc`/`get` views point precisely at one symbol. A unit larger
// than maxChars is line-windowed (each window keeps the unit's title where the
// signature is still visible). Blank units are dropped. Per-unit granularity is
// deliberate for code: the coarse JSONL chunk is re-segmented downstream for
// retrieval, so smaller, well-titled chunks improve precision without cost.
func emitUnits(units []string, maxChars int, titleFn func(string) string) []flatChunk {
	out := make([]flatChunk, 0, len(units))
	for _, u := range units {
		if strings.TrimSpace(u) == "" {
			continue
		}
		if maxChars > 0 && len(u) > maxChars {
			title := titleFn(u)
			for _, w := range chunkLines(u, maxChars) {
				out = append(out, flatChunk{Content: w, Title: title})
			}
			continue
		}
		out = append(out, flatChunk{Content: u, Title: titleFn(u)})
	}
	return out
}

// packGeneric windows arbitrary text by line boundaries, titleless (the source
// path is the only meaningful identity for a config/data file).
func packGeneric(content string, maxChars int) []flatChunk {
	if maxChars <= 0 || len(content) <= maxChars {
		return []flatChunk{{Content: content}}
	}
	windows := chunkLines(content, maxChars)
	out := make([]flatChunk, 0, len(windows))
	for _, w := range windows {
		out = append(out, flatChunk{Content: w})
	}
	return out
}

// splitBraceUnits groups a {}-delimited source into top-level units: each
// balanced top-level block (with the lines leading up to it, e.g. its doc
// comment and signature) becomes one unit; a trailing run of depth-0 lines
// becomes the last unit. Brace counting ignores line comments and quoted spans
// so a `{` inside a string or `// note {` does not skew depth — the ~90% case.
func splitBraceUnits(content string) []string {
	lines := strings.Split(content, "\n")
	var units []string
	buf := make([]string, 0, 32)
	depth := 0
	opened := false
	for _, line := range lines {
		buf = append(buf, line)
		depth += braceDelta(line)
		if depth < 0 {
			depth = 0
		}
		if depth > 0 {
			opened = true
		}
		if depth == 0 && opened {
			units = append(units, strings.Join(buf, "\n"))
			buf = buf[:0]
			opened = false
		}
	}
	if len(buf) > 0 {
		units = append(units, strings.Join(buf, "\n"))
	}
	return units
}

// braceDelta returns the net {} balance of a line after stripping line comments
// and quoted spans (so braces in strings/comments do not move the depth).
func braceDelta(line string) int {
	s := stripBraceNoise(line)
	return strings.Count(s, "{") - strings.Count(s, "}")
}

// stripBraceNoise removes // and # line comments and the contents of single-,
// double- and back-quoted spans from a line, leaving structural braces intact.
func stripBraceNoise(line string) string {
	var b strings.Builder
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '"', '\'', '`':
			quote = c
		case '/':
			if i+1 < len(line) && line[i+1] == '/' {
				return b.String()
			}
			b.WriteByte(c)
		case '#':
			return b.String()
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// splitYAMLDocs splits a YAML stream on document separators (`---` on its own
// line). Front-matter style leading `---` produces no empty leading doc.
func splitYAMLDocs(content string) []string {
	lines := strings.Split(content, "\n")
	var docs []string
	buf := make([]string, 0, 32)
	flush := func() {
		if len(buf) > 0 {
			docs = append(docs, strings.Join(buf, "\n"))
			buf = buf[:0]
		}
	}
	for _, line := range lines {
		if strings.TrimSpace(line) == "---" {
			flush()
			continue
		}
		buf = append(buf, line)
	}
	flush()
	return docs
}

// splitIndentUnits splits Python/Ruby on top-level (column-0) def/class/module
// declarations: each declaration and its indented body up to the next top-level
// declaration is one unit; leading module-level code is its own unit.
func splitIndentUnits(content string) []string {
	lines := strings.Split(content, "\n")
	var units []string
	buf := make([]string, 0, 32)
	for _, line := range lines {
		if topLevelDecl.MatchString(line) && len(buf) > 0 {
			units = append(units, strings.Join(buf, "\n"))
			buf = buf[:0]
		}
		buf = append(buf, line)
	}
	if len(buf) > 0 {
		units = append(units, strings.Join(buf, "\n"))
	}
	return units
}

var (
	// Indentation languages: a top-level (column-0) declaration opens a unit.
	topLevelDecl = regexp.MustCompile(`^(?:async\s+)?(?:def|class|module)\s`)

	// Config-format extractors. HCL and Kubernetes/YAML are REGULAR grammars,
	// so a small anchored regex names them reliably — unlike general code.
	hclSigRe   = regexp.MustCompile(`(?m)^\s*(resource|module|data|variable|output|provider|locals|terraform|backend)\b\s*"?([A-Za-z0-9_.\-/]+)?"?\s*(?:"([A-Za-z0-9_.\-/]+)")?`)
	yamlKindRe = regexp.MustCompile(`(?m)^kind:\s*"?([A-Za-z0-9_.\-]+)"?`)
	yamlNameRe = regexp.MustCompile(`(?m)^\s{0,4}name:\s*"?([A-Za-z0-9_.\-/]+)"?`)
	yamlKey0Re = regexp.MustCompile(`(?m)^([A-Za-z0-9_.\-]+):`)

	// Language-agnostic code-identifier heuristics. NONE of these encode a full
	// per-language grammar; they recognize the universal *shapes* of a
	// declaration and are tried in order. The layering — and the signature-line
	// fallback in codeTitle — is what keeps titling robust across Go, Java,
	// Python, Kotlin, Rust, C#, C/C++, Swift, Scala, TS and the rest without a
	// brittle keyword catalog per language.
	//
	//   goReceiverRe — Go method: func (s *Server) Start(  → Start
	//   declKeywordRe — keyword-led decl across many langs: `<kw> Name`
	//   identCallRe   — name immediately before "(": catches typed-return
	//                   methods (Java/C/C#/Kotlin) the keyword set can't.
	goReceiverRe  = regexp.MustCompile(`^func\s+\([^)]*\)\s+([A-Za-z_]\w*)`)
	declKeywordRe = regexp.MustCompile(`\b(?:func|fun|fn|function|def|class|interface|struct|enum|trait|object|record|namespace|module|impl|protocol|extension|type|service|sub|proc)\s+([A-Za-z_]\w*)`)
	identCallRe   = regexp.MustCompile(`([A-Za-z_]\w*)\s*\(`)

	// preambleRe marks lines that are never a declaration signature (imports,
	// pragmas, annotations) so firstSignatureLine skips past them to the symbol.
	preambleRe = regexp.MustCompile(`^\s*(?:package|import|from|using|use|require|include|#include|#pragma|#define|@|export\s+default|export\s*\{|module\.exports|"use strict"|'use strict')`)
)

// codeTitle derives a robust, language-agnostic label for a code unit. It scans
// the unit's significant lines and prefers, in order:
//
//  1. a STRONG declaration signal — a Go receiver method or a keyword-led
//     declaration (`func`/`class`/`def`/`fun`/…) — returned the moment it is
//     seen, so a leading top-level statement never shadows the real symbol;
//  2. a WEAK signal — the first name immediately before "(", which catches
//     typed-return methods (Java/C/C#/Kotlin) the keyword set cannot name;
//  3. the cleaned first signature line verbatim, when nothing parses.
//
// A title is therefore never silently wrong: at worst it is the verbatim
// signature, and the chunk CONTENT is always indexed regardless — an
// unrecognized language costs a prettier label, never retrievability. Returns
// "" only for a unit with no signature line at all (e.g. a pure import
// preamble), where the source path is identity enough.
func codeTitle(block string) string {
	var firstSig, weak string
	for _, raw := range strings.Split(block, "\n") {
		sig := cleanSignatureLine(raw)
		if sig == "" {
			continue
		}
		if firstSig == "" {
			firstSig = sig
		}
		if id := strongIdent(sig); id != "" {
			return id
		}
		if weak == "" {
			if m := identCallRe.FindStringSubmatch(sig); len(m) == 2 {
				weak = m[1]
			}
		}
	}
	if weak != "" {
		return weak
	}
	return firstSig
}

// strongIdent returns the declared name when a line is unambiguously a
// declaration (Go receiver method or keyword-led decl), else "".
func strongIdent(sig string) string {
	for _, re := range []*regexp.Regexp{goReceiverRe, declKeywordRe} {
		if m := re.FindStringSubmatch(sig); len(m) == 2 {
			return m[1]
		}
	}
	return ""
}

// cleanSignatureLine normalizes one source line into a candidate signature, or
// returns "" when the line is blank, a comment, or import/pragma preamble.
// Comment/string noise and trailing block-opener punctuation are removed and
// whitespace collapsed — structurally, not per language.
func cleanSignatureLine(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || preambleRe.MatchString(raw) ||
		strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "/*") ||
		strings.HasPrefix(trimmed, "<!--") {
		return ""
	}
	clean := strings.TrimRight(strings.TrimSpace(stripBraceNoise(raw)), " \t{(")
	if clean == "" {
		return "" // the line was comment-only
	}
	return clampTitle(clean)
}

// clampTitle collapses whitespace and bounds a title to a sane display length.
func clampTitle(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	const maxTitleLen = 80
	if len(s) > maxTitleLen {
		s = strings.TrimSpace(s[:maxTitleLen])
	}
	return s
}

// hclTitle renders a Terraform/HCL block title as "type.name" (or just the
// block type for unnamed blocks like terraform/locals).
func hclTitle(block string) string {
	m := hclSigRe.FindStringSubmatch(block)
	if len(m) == 0 {
		return ""
	}
	switch {
	case m[2] != "" && m[3] != "":
		return m[2] + "." + m[3]
	case m[3] != "":
		return m[1] + "." + m[3]
	case m[2] != "":
		return m[1] + "." + m[2]
	default:
		return m[1]
	}
}

// yamlTitle identifies a manifest as "Kind/name" when both are present, else
// the Kind, the name, or the first top-level key — whatever locates the doc.
func yamlTitle(doc string) string {
	kind := firstSubmatch(yamlKindRe, doc)
	name := firstSubmatch(yamlNameRe, doc)
	switch {
	case kind != "" && name != "":
		return kind + "/" + name
	case kind != "":
		return kind
	case name != "":
		return name
	default:
		return firstSubmatch(yamlKey0Re, doc)
	}
}

func firstSubmatch(re *regexp.Regexp, s string) string {
	if m := re.FindStringSubmatch(s); len(m) == 2 {
		return m[1]
	}
	return ""
}

// normalizeNewlines collapses CRLF/CR to LF so chunk boundaries and brace
// counting see a single newline convention.
func normalizeNewlines(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.ReplaceAll(text, "\r", "\n")
}
