/*
 * ChatCLI - Quote Normalization
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Normalizes curly/smart quotes to straight quotes before write/patch operations.
 * LLMs frequently generate curly quotes (', ', ", ") which cause compilation
 * errors in source code.
 *
 * Inspired by openclaude's quote normalization in FileEditTool which preserves
 * file typography style. Here we take a simpler approach: always normalize to
 * straight quotes for code files, preserving curly quotes only in documentation.
 */
package agent

import (
	"path/filepath"
	"strings"
)

// Curly/smart quote Unicode characters
const (
	LeftSingleCurly  = '\u2018' // '
	RightSingleCurly = '\u2019' // '
	LeftDoubleCurly  = '\u201C' // "
	RightDoubleCurly = '\u201D' // "
	LeftAngleDouble  = '\u00AB' // «
	RightAngleDouble = '\u00BB' // »
	PrimeSingle      = '\u2032' // ′
	PrimeDouble      = '\u2033' // ″
)

// codeFileExtensions are file types where curly quotes are never valid.
var codeFileExtensions = map[string]bool{
	".go": true, ".py": true, ".js": true, ".ts": true, ".jsx": true, ".tsx": true,
	".java": true, ".c": true, ".cpp": true, ".h": true, ".hpp": true,
	".rs": true, ".rb": true, ".php": true, ".swift": true, ".kt": true,
	".scala": true, ".cs": true, ".r": true, ".m": true, ".mm": true,
	".sh": true, ".bash": true, ".zsh": true, ".fish": true,
	".sql": true, ".json": true, ".yaml": true, ".yml": true, ".toml": true,
	".xml": true, ".html": true, ".css": true, ".scss": true, ".less": true,
	".lua": true, ".pl": true, ".pm": true, ".ex": true, ".exs": true,
	".erl": true, ".hrl": true, ".hs": true, ".elm": true, ".clj": true,
	".lisp": true, ".el": true, ".vim": true, ".conf": true, ".ini": true,
	".env": true, ".dockerfile": true, ".tf": true, ".hcl": true,
	".proto": true, ".graphql": true, ".gql": true,
	".makefile": true, ".cmake": true, ".gradle": true,
}

// NormalizeQuotes replaces curly/smart quotes with straight ASCII quotes.
// Only applies to code files — documentation files (.md, .txt, .rst) are unchanged.
func NormalizeQuotes(content, filePath string) string {
	if !isCodeFile(filePath) {
		return content
	}
	return normalizeAllQuotes(content)
}

// NormalizeQuotesAlways replaces curly quotes regardless of file type.
// Use this for tool call arguments where curly quotes are always wrong.
func NormalizeQuotesAlways(content string) string {
	return normalizeAllQuotes(content)
}

// DetectCurlyQuotes checks if the content contains any curly/smart quotes.
// Returns the count and positions for diagnostics.
func DetectCurlyQuotes(content string) (count int, positions []int) {
	for i, r := range content {
		if isCurlyQuote(r) {
			count++
			positions = append(positions, i)
		}
	}
	return
}

// HasSuspiciousUnicode checks for Unicode characters that look like ASCII but aren't.
// Returns true if any lookalike characters are found.
func HasSuspiciousUnicode(content string) bool {
	for _, r := range content {
		if isCurlyQuote(r) || isUnicodeLookalike(r) {
			return true
		}
	}
	return false
}

func normalizeAllQuotes(content string) string {
	var b strings.Builder
	b.Grow(len(content))

	for _, r := range content {
		switch r {
		case LeftSingleCurly, RightSingleCurly, PrimeSingle:
			b.WriteByte('\'')
		case LeftDoubleCurly, RightDoubleCurly, PrimeDouble:
			b.WriteByte('"')
		case LeftAngleDouble:
			b.WriteString("<<")
		case RightAngleDouble:
			b.WriteString(">>")
		default:
			b.WriteRune(r)
		}
	}

	return b.String()
}

func isCurlyQuote(r rune) bool {
	switch r {
	case LeftSingleCurly, RightSingleCurly,
		LeftDoubleCurly, RightDoubleCurly,
		LeftAngleDouble, RightAngleDouble,
		PrimeSingle, PrimeDouble:
		return true
	}
	return false
}

// isUnicodeLookalike checks for Unicode characters commonly confused with ASCII.
func isUnicodeLookalike(r rune) bool {
	switch r {
	case '\u00A0': // non-breaking space
		return true
	case '\u2013', '\u2014': // en-dash, em-dash (vs hyphen)
		return true
	case '\u2026': // ellipsis (vs ...)
		return true
	case '\u00D7': // multiplication sign (vs x)
		return true
	case '\u2212': // minus sign (vs hyphen-minus)
		return true
	}
	return false
}

func isCodeFile(path string) bool {
	if path == "" {
		return true // when in doubt, normalize
	}
	ext := strings.ToLower(filepath.Ext(path))
	if codeFileExtensions[ext] {
		return true
	}
	// Check filename without extension
	base := strings.ToLower(filepath.Base(path))
	switch base {
	case "makefile", "dockerfile", "jenkinsfile", "vagrantfile",
		"gemfile", "rakefile", "guardfile", "procfile",
		".gitignore", ".dockerignore", ".eslintrc", ".prettierrc":
		return true
	}
	return false
}
