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
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/diillson/chatcli/pkg/persona"
)

// skillInjectBudgetEnvVar caps the total characters of skill BODIES inlined
// per injection block. Skills beyond the budget degrade to an index entry
// (name, description, source path) the model reads on demand — nothing is
// lost, only deferred. 0 disables the cap (legacy inline-everything).
//
// Rationale: a burst of auto-activated skills (e.g. nine ServiceNow skills
// matching one prompt) can add tens of KB to EVERY request — the payload
// floor that trips corporate proxy/WAF body caps.
const skillInjectBudgetEnvVar = "CHATCLI_SKILL_INJECT_BUDGET"

// defaultSkillInjectBudget is generous: typical sessions inline every
// activated skill untouched; only pathological bursts overflow to index
// entries.
const defaultSkillInjectBudget = 24_000

// skillInjectBudget resolves the per-block skill body budget from the
// environment, read live so /config flips apply immediately.
func skillInjectBudget() int {
	v := os.Getenv(skillInjectBudgetEnvVar)
	if v == "" {
		return defaultSkillInjectBudget
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return defaultSkillInjectBudget
	}
	return n
}

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

	// 3. Bare tokens with a slash or known extension — shared with the
	//    flat-prompt surfaces (gRPC server/operator) via pkg/persona.
	for _, tok := range persona.ExtractPathTokens(input) {
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
	return buildSkillInjectionBlockLimited(skills, skillInjectBudget())
}

// buildSkillInjectionBlockLimited is buildSkillInjectionBlock under an
// explicit body budget (the prompt budget may hand it less than the
// per-block default).
func buildSkillInjectionBlockLimited(skills []*persona.Skill, budget int) string {
	if len(skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("# Auto-loaded Skills\n\n")
	b.WriteString("The following skills were automatically activated based on ")
	b.WriteString("your input (matched via `triggers:` keywords or `paths:` globs ")
	b.WriteString("in the skill frontmatter). Follow their guidance when relevant.\n\n")
	renderSkillEntriesLimited(&b, skills, budget, false)
	return b.String()
}

// renderSkillEntries writes the shared per-skill markdown section (name,
// version, description, content) used by every skill injection block.
//
// Bodies are inlined until the per-block budget (skillInjectBudget) is
// spent; skills past that point keep their header and description but their
// body degrades to a read-on-demand pointer at the skill's source path.
// Headers and descriptions are always inlined — the model must know a skill
// activated even when its body is deferred. Callers pass skills in a stable
// order, so which skills overflow is deterministic and cache-friendly.
func renderSkillEntries(b *strings.Builder, skills []*persona.Skill) {
	renderSkillEntriesLimited(b, skills, skillInjectBudget(), false)
}

// renderSkillEntriesLimited is renderSkillEntries with the budget made
// explicit and an optional deferAll switch that forces every body into the
// read-on-demand pointer form. deferAll serves the per-Run skill budget in
// agent mode: once a run has already injected its quota of skill bytes,
// later activations still announce themselves (header + description +
// source) without paying for another full body.
func renderSkillEntriesLimited(b *strings.Builder, skills []*persona.Skill, budget int, deferAll bool) {
	spent := 0
	for _, skill := range skills {
		fmt.Fprintf(b, "## Skill: %s", skill.Name)
		if skill.Version != "" {
			fmt.Fprintf(b, " (v%s)", skill.Version)
		}
		b.WriteString("\n\n")
		if skill.Description != "" {
			b.WriteString(skill.Description)
			b.WriteString("\n\n")
		}
		// Always cite the source file: the model (and any later reader of an
		// aged/collapsed copy of this block) can re-read the full guidance
		// on demand. Stable per skill, so the line is cache-friendly.
		if skill.Path != "" {
			fmt.Fprintf(b, "_Source: %s_\n\n", skill.Path)
		}
		body := strings.TrimSpace(skill.Content)
		if body == "" {
			continue
		}
		if deferAll || (budget > 0 && spent+len(body) > budget) {
			b.WriteString(renderSkillBodyPointer(skill))
			continue
		}
		b.WriteString(body)
		b.WriteString("\n\n")
		spent += len(body)
	}
}

// renderSkillBodyPointer emits the deferred-body note for a skill whose
// content did not fit the injection budget. English on purpose — this is
// model-facing prompt text, like every other block in this file.
func renderSkillBodyPointer(skill *persona.Skill) string {
	if skill.Path != "" {
		return fmt.Sprintf("_Body not inlined (skill injection budget). "+
			"Before applying this skill, read its full instructions from: %s_\n\n", skill.Path)
	}
	return "_Body not inlined (skill injection budget) and no source file is " +
		"available. Rely on the description above; do not invent instructions._\n\n"
}

// buildPinnedSkillInjectionBlock formats user-pinned skills (via `/skill pin`)
// into a dedicated system-prompt block. Kept separate from the auto-activated
// block so:
//   - the LLM can tell explicit user intent (pin) apart from heuristic activation;
//   - the pin block stays stable across turns (only pin/unpin invalidate cache)
//     while the auto block churns with user input.
//
// Caller must pass a slice already sorted by Name for cache stability.
func buildPinnedSkillInjectionBlock(skills []*persona.Skill) string {
	return buildPinnedSkillInjectionBlockLimited(skills, skillInjectBudget())
}

// buildPinnedSkillInjectionBlockLimited is buildPinnedSkillInjectionBlock
// under an explicit body budget.
func buildPinnedSkillInjectionBlockLimited(skills []*persona.Skill, budget int) string {
	if len(skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("# Pinned Skills\n\n")
	b.WriteString("The user pinned the following skills for this session via ")
	b.WriteString("`/skill pin <name>`. They apply to every turn regardless of ")
	b.WriteString("input triggers or file paths — treat them as standing instructions.\n\n")
	renderSkillEntriesLimited(&b, skills, budget, false)
	return b.String()
}

// dedupAutoAgainstPinned filters an auto-activated skill slice to exclude any
// skill whose Name is already in the pinned set, preventing the same skill
// from being injected twice (once under "Pinned" and once under "Auto-loaded").
func dedupAutoAgainstPinned(autoSkills, pinnedSkills []*persona.Skill) []*persona.Skill {
	if len(pinnedSkills) == 0 || len(autoSkills) == 0 {
		return autoSkills
	}
	pinned := make(map[string]struct{}, len(pinnedSkills))
	for _, s := range pinnedSkills {
		pinned[s.Name] = struct{}{}
	}
	out := make([]*persona.Skill, 0, len(autoSkills))
	for _, s := range autoSkills {
		if _, dup := pinned[s.Name]; dup {
			continue
		}
		out = append(out, s)
	}
	return out
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
