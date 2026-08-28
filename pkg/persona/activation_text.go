/*
 * ChatCLI - Persona System
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * activation_text.go — flat-prompt skill activation helpers.
 *
 * The interactive surfaces (REPL, one-shot, MCP/ACP) compose skills into
 * structured system-prompt blocks inside the cli package. Surfaces that only
 * have a flat prompt string to work with — the gRPC server serving remote
 * clients and the Kubernetes operator's analysis requests — need the same
 * auto-activation without importing cli. This file hosts the shared pieces:
 * bare file-path token extraction (feeds `paths:` glob matching) and a
 * prompt-preamble renderer for activated skills.
 */
package persona

import (
	"fmt"
	"regexp"
	"strings"
)

// DefaultActivationBudget caps the total characters of skill BODIES inlined
// in one activation block. Skills past the budget keep their header and
// description; the body degrades to a read-on-demand pointer.
const DefaultActivationBudget = 24_000

// pathTokenRe matches bare file-like tokens inside free text. A token must
// contain either a slash (clearly a path) or a recognized source/config
// extension, so prose almost never false-positives.
var pathTokenRe = regexp.MustCompile(`(?:\./|~/|/)?[A-Za-z0-9_.\-]+(?:/[A-Za-z0-9_.\-]+)+|[A-Za-z0-9_.\-]+\.(?:go|ts|tsx|js|jsx|py|rs|java|kt|rb|php|cs|cpp|cc|c|h|hpp|md|mdx|json|ya?ml|toml|sh|bash|zsh|sql|proto|tf|dockerfile|lock|mod|sum|css|scss|html?)`)

// ExtractPathTokens returns a deduplicated, forward-slash-normalized list of
// bare file-path tokens present in text. The result feeds
// Manager.FindPathMatchedSkills — it is never used to read files, so
// non-existent paths are kept (a skill may match paths being planned).
func ExtractPathTokens(text string) []string {
	if text == "" {
		return nil
	}
	matches := pathTokenRe.FindAllString(text, -1)
	seen := make(map[string]bool, len(matches))
	out := make([]string, 0, len(matches))
	for _, tok := range matches {
		tok = strings.ReplaceAll(strings.TrimSpace(tok), "\\", "/")
		tok = strings.Trim(tok, ".,;:()[]{}\"'`")
		if tok == "" || seen[tok] {
			continue
		}
		seen[tok] = true
		out = append(out, tok)
	}
	return out
}

// BuildActivationPromptBlock renders auto-activated skills as a prompt
// preamble for flat-prompt surfaces. Bodies are inlined until budget is
// spent (0 disables the cap); overflowing skills keep name + description
// plus a pointer to their source file. Model-facing English by design.
func BuildActivationPromptBlock(skills []*Skill, budget int) string {
	if len(skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("# Auto-loaded Skills\n\n")
	b.WriteString("The following skills were automatically activated by the request ")
	b.WriteString("content (matched via `triggers:` keywords or `paths:` globs in the ")
	b.WriteString("skill frontmatter). Follow their guidance when relevant.\n\n")
	spent := 0
	for _, s := range skills {
		fmt.Fprintf(&b, "## Skill: %s", s.Name)
		if s.Version != "" {
			fmt.Fprintf(&b, " (v%s)", s.Version)
		}
		b.WriteString("\n\n")
		if s.Description != "" {
			b.WriteString(s.Description)
			b.WriteString("\n\n")
		}
		body := strings.TrimSpace(s.Content)
		if body == "" {
			continue
		}
		if budget > 0 && spent+len(body) > budget {
			if s.Path != "" {
				fmt.Fprintf(&b, "_Body not inlined (skill budget). Full instructions at: %s_\n\n", s.Path)
			} else {
				b.WriteString("_Body not inlined (skill budget) and no source file is available. Rely on the description above; do not invent instructions._\n\n")
			}
			continue
		}
		b.WriteString(body)
		b.WriteString("\n\n")
		spent += len(body)
	}
	return b.String()
}
