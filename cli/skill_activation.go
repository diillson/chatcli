/*
 * ChatCLI - Skill auto-activation helpers
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Extracts file-like tokens from user input and composes the system-prompt
 * block used to inject auto-triggered / path-matched skills. Shared by chat
 * mode (cli_llm.go) and agent mode (agent_mode.go) so that both code paths
 * honor the same skill frontmatter contract.
 */
package cli

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/diillson/chatcli/pkg/persona"
)

// filePathTokenRe matches bare file-like tokens inside a user message.
//
// It is intentionally stricter than pathMentionRe: here we do not require a
// leading "@" because the intent is to detect casual mentions like
// "run go test on pkg/foo/bar_test.go" or "look at src/index.ts". To avoid
// false positives on prose, a token must contain either:
//   - a slash (so we're clearly looking at a path), OR
//   - a recognized extension (.go, .ts, .py, .md, etc.) with no whitespace
//
// We match a superset and filter in code for extension validity.
var filePathTokenRe = regexp.MustCompile(`(?:\./|~/|/)?[A-Za-z0-9_.\-]+(?:/[A-Za-z0-9_.\-]+)+|[A-Za-z0-9_.\-]+\.(?:go|ts|tsx|js|jsx|py|rs|java|kt|rb|php|cs|cpp|cc|c|h|hpp|md|mdx|json|ya?ml|toml|sh|bash|zsh|sql|proto|tf|dockerfile|lock|mod|sum|css|scss|html?)`)

// extractFilePaths returns a deduplicated, forward-slash-normalized list of
// file-path tokens present in the input. It looks at three sources:
//
//  1. `@file <path>` commands (existing chatcli syntax)
//  2. `@./foo/bar.go` path mentions (pathMentionRe)
//  3. Bare file tokens like "pkg/foo/bar_test.go" or "main.go"
//
// The result feeds `Manager.FindPathMatchedSkills` — it is NOT used to read
// files, only to match glob patterns, so non-existent paths are kept (a skill
// author might want to match on paths the user is *planning* to create).
func extractFilePaths(input string) []string {
	if input == "" {
		return nil
	}
	seen := make(map[string]bool)
	var out []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		p = strings.ReplaceAll(p, "\\", "/")
		// Strip surrounding punctuation commonly found in prose.
		p = strings.Trim(p, ".,;:()[]{}\"'`")
		if p == "" {
			return
		}
		if seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}

	// 1. @file <path> extraction (reuse existing regex).
	//    We purposely do not require the path to exist on disk — skills can
	//    match prospective paths.
	fileCmdRe := regexp.MustCompile(`@file\s+([\w./_~\-*]+/?[\w.\-*]*)`)
	for _, m := range fileCmdRe.FindAllStringSubmatch(input, -1) {
		if len(m) > 1 {
			add(m[1])
		}
	}

	// 2. @path mentions (existing pathMentionRe lives in path_mentions.go).
	for _, m := range pathMentionRe.FindAllStringSubmatch(input, -1) {
		if len(m) > 1 {
			add(m[1])
		}
	}

	// 3. Bare tokens with a slash or known extension.
	for _, tok := range filePathTokenRe.FindAllString(input, -1) {
		add(tok)
	}

	return out
}

// buildSkillInjectionBlock formats a slice of auto-activated skills into the
// system-prompt block that gets appended as a ContentBlock. Returns an empty
// string when `skills` is empty so the caller can skip injection cleanly.
//
// The block uses a stable header so provider-level caching can reuse it across
// turns when the same skills fire.
func buildSkillInjectionBlock(skills []*persona.Skill) string {
	if len(skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("# Auto-loaded Skills\n\n")
	b.WriteString("The following skills were automatically activated based on ")
	b.WriteString("your input (matched via `triggers:` keywords or `paths:` globs ")
	b.WriteString("in the skill frontmatter). Follow their guidance when relevant.\n\n")
	for _, skill := range skills {
		fmt.Fprintf(&b, "## Skill: %s", skill.Name)
		if skill.Version != "" {
			fmt.Fprintf(&b, " (v%s)", skill.Version)
		}
		b.WriteString("\n\n")
		if skill.Description != "" {
			b.WriteString(skill.Description)
			b.WriteString("\n\n")
		}
		if strings.TrimSpace(skill.Content) != "" {
			b.WriteString(skill.Content)
			b.WriteString("\n\n")
		}
	}
	return b.String()
}

// pickSkillModelAndEffort selects a model/effort hint from the auto-activated
// skills for a single turn. Rules:
//
//   - The first skill with a non-empty `model:` wins for model.
//   - The first skill with a non-empty `effort:` wins for effort.
//   - Skills are iterated in the order they were returned
//     (triggers first, then paths — see Manager.FindAutoActivatedSkills).
//
// If two skills disagree on model, only the first one is honored and a
// warning is logged by the caller (we return the losing name for diagnostics).
func pickSkillModelAndEffort(skills []*persona.Skill) (model, effort, conflictName string) {
	for _, s := range skills {
		if model == "" && strings.TrimSpace(s.Model) != "" {
			model = strings.TrimSpace(s.Model)
		} else if strings.TrimSpace(s.Model) != "" && !strings.EqualFold(s.Model, model) && conflictName == "" {
			conflictName = s.Name
		}
		if effort == "" && strings.TrimSpace(s.Effort) != "" {
			effort = strings.ToLower(strings.TrimSpace(s.Effort))
		}
	}
	return model, effort, conflictName
}
