/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package compress

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"strings"
)

// CodeCompressor reduces source code to a structural skeleton — package/imports,
// type/const/var declarations, and function *signatures* — eliding function
// bodies. It is the compressor behind Headroom's "codebase exploration" win:
// when surveying many files the model needs the shape (what exists, what calls
// what), not every statement. The full source is offloaded to CCR.
//
// SAFETY — this compressor NEVER auto-fires on tool output. Dropping the body
// of a file an agent is about to edit would be actively harmful (the agent
// reads it precisely to see/modify the implementation). It therefore only
// engages when the caller explicitly asks for code compression via
// Hint.MIME=="code" (e.g. the @compress tool with hint=code, or an explicit
// codebase-survey path). The automatic tool-output path leaves code untouched.
//
// Go is compressed precisely via go/ast (always valid output). Other languages
// use a conservative brace/indent heuristic that keeps declaration-shaped lines
// and drops nested bodies.
type CodeCompressor struct{}

// NewCodeCompressor returns a ready compressor.
func NewCodeCompressor() *CodeCompressor { return &CodeCompressor{} }

// Name implements Compressor.
func (*CodeCompressor) Name() string { return "code-ast" }

// Detect implements Compressor. It returns confidence ONLY when the caller
// explicitly flags the content as code; it never sniffs content, so it can
// never be auto-selected for ordinary tool output.
func (c *CodeCompressor) Detect(_ string, h Hint) float64 {
	if strings.EqualFold(strings.TrimSpace(h.MIME), "code") {
		return 0.95
	}
	return 0
}

// Compress implements Compressor.
func (c *CodeCompressor) Compress(content string, opts Options) (Result, error) {
	if !canDrop(opts) {
		// Skeletonization is lossy (bodies removed); without CCR we must not
		// drop anything.
		return passthrough(content), nil
	}

	skeleton, lang, ok := goSkeleton(content)
	if !ok {
		skeleton, lang, ok = heuristicSkeleton(content)
	}
	if !ok || len(skeleton) >= len(content) {
		return passthrough(content), nil
	}

	marker, key := offload(content, opts)
	if marker == "" {
		return passthrough(content), nil
	}
	out := skeleton + fmt.Sprintf("\n// [code skeleton (%s): full source recoverable with @recall %s]\n", lang, marker)
	return Result{
		Compressed:     out,
		OriginalSize:   len(content),
		CompressedSize: len(out),
		Strategy:       c.Name(),
		CacheKey:       key,
		Reversible:     true,
		Detail:         map[string]int{"skeleton_bytes": len(skeleton)},
	}, nil
}

// goSkeleton parses Go source and renders package + imports + type/const/var
// declarations verbatim and function signatures with elided bodies. Returns
// ok=false when the input isn't parseable Go (caller falls back to heuristics).
func goSkeleton(src string) (skeleton, lang string, ok bool) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.ParseComments)
	if err != nil {
		return "", "", false
	}
	cfg := &printer.Config{Mode: printer.UseSpaces | printer.TabIndent, Tabwidth: 4}

	var b strings.Builder
	fmt.Fprintf(&b, "package %s\n\n", f.Name.Name)

	for _, d := range f.Decls {
		switch decl := d.(type) {
		case *ast.FuncDecl:
			// Print the signature only by temporarily detaching the body.
			body := decl.Body
			decl.Body = nil
			if err := cfg.Fprint(&b, fset, decl); err == nil {
				// Empty block with a comment keeps the skeleton valid Go.
				b.WriteString(" { /* ... */ }\n\n")
			}
			decl.Body = body
		case *ast.GenDecl:
			// imports / type / const / var — kept verbatim (they carry the
			// API surface and are usually small).
			if err := cfg.Fprint(&b, fset, decl); err == nil {
				b.WriteString("\n\n")
			}
		}
	}
	return b.String(), "go", true
}

// structuralKeywords introduce a named code unit (kept even when indented, so
// nested methods/classes survive). Deliberately excludes const/let/var/public/
// static — those appear in statement bodies too and would defeat the skeleton.
var structuralKeywords = []string{
	"func ", "func(", "function ", "def ", "class ", "interface ", "struct ",
	"enum ", "type ", "trait ", "impl ", "fn ", "module ", "namespace ",
	"object ", "record ", "protocol ", "extension ", "sub ", "method ",
}

// controlKeywords open a block but are NOT declarations; their bodies are noise
// for a skeleton, so block-opener lines starting with these are dropped.
var controlKeywords = []string{
	"if", "for", "while", "switch", "else", "catch", "try", "do", "foreach",
	"elif", "elsif", "with", "when", "match", "case", "finally", "loop", "until",
}

// heuristicSkeleton extracts a declaration skeleton from non-Go source. It keeps
// top-level (unindented) lines, structural declarations at any depth, and
// non-control block openers (method signatures), eliding statement bodies.
// Language-agnostic and conservative; works for brace languages and
// indentation languages (Python/Ruby) alike.
func heuristicSkeleton(src string) (skeleton, lang string, ok bool) {
	lines := strings.Split(src, "\n")
	if len(lines) < 10 {
		return "", "", false
	}
	var b strings.Builder
	run := 0          // dropped lines in the current contiguous run
	totalDropped := 0 // dropped lines overall (run is reset by flush)
	keepCount := 0
	flush := func() {
		if run > 0 {
			fmt.Fprintf(&b, "    // ... %d line(s) elided ...\n", run)
			run = 0
		}
	}
	for _, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		topLevel := ln == "" || (ln[0] != ' ' && ln[0] != '\t')
		keep := topLevel ||
			trimmed == "}" || trimmed == "" ||
			hasStructuralPrefix(trimmed) ||
			(strings.HasSuffix(trimmed, "{") && !hasControlPrefix(trimmed))
		if keep {
			flush()
			b.WriteString(ln)
			b.WriteByte('\n')
			keepCount++
		} else {
			run++
			totalDropped++
		}
	}
	flush()
	if keepCount == 0 || totalDropped == 0 {
		return "", "", false // nothing elided — no point claiming a reduction
	}
	return b.String(), "generic", true
}

func hasStructuralPrefix(trimmed string) bool {
	low := strings.ToLower(trimmed)
	for _, kw := range structuralKeywords {
		if strings.HasPrefix(low, kw) {
			return true
		}
	}
	return false
}

// hasControlPrefix reports whether a block-opening line begins with a control
// keyword (delimited by a non-letter), e.g. "if (x) {" or "for(...) {".
func hasControlPrefix(trimmed string) bool {
	low := strings.ToLower(trimmed)
	for _, kw := range controlKeywords {
		if strings.HasPrefix(low, kw) {
			rest := low[len(kw):]
			if rest == "" || !isAlpha(rest[0]) {
				return true
			}
		}
	}
	return false
}
