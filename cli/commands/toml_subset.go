/*
 * ChatCLI - Minimal TOML subset parser for Gemini/Qwen command files
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Gemini CLI (and its Qwen Code fork) define custom commands as TOML files
 * whose useful surface is exactly two string keys:
 *
 *   description = "one line"
 *   prompt = """
 *   multi-line template
 *   """
 *
 * ChatCLI has no TOML dependency and the house rule is to avoid adding one
 * for a two-key format — this parser covers the string shapes those files
 * actually use (basic "..." with escapes, literal '...', and their
 * triple-quoted multi-line forms) and rejects anything it cannot parse
 * UNAMBIGUOUSLY: a malformed file surfaces in /config commands diagnostics
 * instead of loading with mangled content. Unknown keys with parseable
 * values are skipped; tables ([section]) end the scan — Gemini command
 * files do not use them.
 */
package commands

import (
	"fmt"
	"strings"
)

// geminiCommandTOML is the parsed subset.
type geminiCommandTOML struct {
	Prompt      string
	Description string
	Mode        string
}

// parseCommandTOML extracts prompt/description from a Gemini-style command
// file. Errors are precise so the skipped-file diagnostics stay actionable.
func parseCommandTOML(content string) (geminiCommandTOML, error) {
	var out geminiCommandTOML
	lines := strings.Split(content, "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			// Tables are outside the subset; nothing Gemini commands need
			// lives past one.
			break
		}
		key, rest, ok := strings.Cut(line, "=")
		if !ok {
			return out, fmt.Errorf("toml line %d: expected key = value", i+1)
		}
		key = strings.TrimSpace(key)
		rest = strings.TrimSpace(rest)

		value, consumed, err := parseTOMLString(rest, lines, i)
		if err != nil {
			return out, fmt.Errorf("toml key %q (line %d): %w", key, i+1, err)
		}
		i = consumed

		switch key {
		case "prompt":
			out.Prompt = value
		case "description":
			out.Description = value
		case "mode":
			out.Mode = value
		}
	}
	if strings.TrimSpace(out.Prompt) == "" {
		return out, fmt.Errorf("toml: missing required key %q", "prompt")
	}
	return out, nil
}

// parseTOMLString parses the value starting at rest (the text after '='),
// consuming continuation lines for triple-quoted strings. Returns the value
// and the index of the last consumed line.
func parseTOMLString(rest string, lines []string, start int) (string, int, error) {
	switch {
	case strings.HasPrefix(rest, `"""`):
		return parseTripleQuoted(rest, lines, start, `"""`, true)
	case strings.HasPrefix(rest, "'''"):
		return parseTripleQuoted(rest, lines, start, "'''", false)
	case strings.HasPrefix(rest, `"`):
		return parseBasicString(rest, start)
	case strings.HasPrefix(rest, "'"):
		end := strings.Index(rest[1:], "'")
		if end < 0 {
			return "", start, fmt.Errorf("unterminated literal string")
		}
		return rest[1 : 1+end], start, nil
	default:
		return "", start, fmt.Errorf("unsupported value shape %q (only strings are in the subset)", truncateForErr(rest))
	}
}

// parseBasicString handles a single-line "..." value with TOML basic escapes.
func parseBasicString(rest string, line int) (string, int, error) {
	var b strings.Builder
	escaped := false
	for i := 1; i < len(rest); i++ {
		ch := rest[i]
		if escaped {
			switch ch {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case '"', '\\':
				b.WriteByte(ch)
			default:
				return "", line, fmt.Errorf("unsupported escape \\%c", ch)
			}
			escaped = false
			continue
		}
		switch ch {
		case '\\':
			escaped = true
		case '"':
			return b.String(), line, nil
		default:
			b.WriteByte(ch)
		}
	}
	return "", line, fmt.Errorf("unterminated basic string")
}

// parseTripleQuoted handles """...""" and ”'...”' possibly spanning
// lines. TOML trims a newline immediately after the opening delimiter;
// basic (double-quoted) strings honor the line-ending backslash but the
// subset keeps escapes literal inside multi-line bodies — Gemini prompt
// templates rely on verbatim content.
func parseTripleQuoted(rest string, lines []string, start int, delim string, _ bool) (string, int, error) {
	body := rest[len(delim):]
	// Single-line form: prompt = """all on one line"""
	if end := strings.Index(body, delim); end >= 0 {
		return body[:end], start, nil
	}
	var b strings.Builder
	if body != "" {
		b.WriteString(body)
		b.WriteByte('\n')
	}
	for i := start + 1; i < len(lines); i++ {
		if end := strings.Index(lines[i], delim); end >= 0 {
			b.WriteString(lines[i][:end])
			return strings.TrimPrefix(b.String(), "\n"), i, nil
		}
		b.WriteString(lines[i])
		b.WriteByte('\n')
	}
	return "", start, fmt.Errorf("unterminated %s string", delim)
}

func truncateForErr(s string) string {
	if len(s) > 40 {
		return s[:40] + "…"
	}
	return s
}
