/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Phase 2 (#2) — Plan-First trigger heuristic.
 *
 * The complexity score decides whether the auto Plan-First mode should
 * synthesize a structured plan before the orchestrator dispatches. The
 * scorer is intentionally conservative: it errs toward letting the
 * orchestrator handle simple tasks directly (no extra LLM call) and
 * triggers planning only when the task carries multi-step or multi-file
 * signals.
 */
package quality

import (
	"regexp"
	"strings"
)

// actionVerbs is the closed set of imperative verbs that signal a task
// has multiple discrete actions. The list is bilingual (en + pt-BR) so
// the heuristic works across the chatcli locales without translation
// at query time. Verbs are matched as whole words against a lower-cased
// task string.
var actionVerbs = map[string]struct{}{
	// English
	"implement": {}, "add": {}, "create": {}, "fix": {}, "refactor": {},
	"write": {}, "test": {}, "deploy": {}, "build": {}, "run": {},
	"update": {}, "remove": {}, "delete": {}, "rename": {}, "migrate": {},
	"setup": {}, "configure": {}, "validate": {}, "verify": {}, "review": {},
	"document": {}, "split": {}, "merge": {}, "extract": {}, "rewrite": {},
	// Portuguese
	"implementar": {}, "adicionar": {}, "criar": {}, "corrigir": {},
	"escrever": {}, "testar": {}, "atualizar": {}, "remover": {},
	"renomear": {}, "configurar": {}, "validar": {}, "verificar": {},
	"revisar": {}, "documentar": {}, "dividir": {}, "extrair": {},
	"reescrever": {},
}

// sequencers are connector tokens that signal "do X, then Y". When one
// task mentions multiple actions joined by these, ReWOO planning pays
// off because the steps have explicit ordering.
var sequencers = []string{
	" and then ", " e depois ", " then ", " depois ", " e em seguida ",
	" finally ", " por fim ", " after ", " após ",
}

// fileExtensionRE matches file path tokens like "main.go", "src/foo.ts",
// "Dockerfile" — anything that looks like a concrete artefact the
// orchestrator would need to coordinate.
var fileExtensionRE = regexp.MustCompile(`\b[\w./-]+\.(go|ts|tsx|js|jsx|py|rb|rs|java|kt|c|cpp|h|hpp|cs|swift|md|json|yaml|yml|toml|sql|sh|bash|zsh)\b`)

// dockerfilesRE catches the common no-extension artefacts so they count
// as "concrete files" too.
var dockerfilesRE = regexp.MustCompile(`\b(Dockerfile|Makefile|CHANGELOG|README|LICENSE|Procfile)\b`)

// ComplexityScore returns a value in [0, 10] estimating how much a task
// would benefit from up-front planning. The score blends three signals:
//
//  - distinct action verbs (capped at 5 contributions)
//  - distinct file artefacts mentioned (capped at 3)
//  - sequencer tokens that imply ordered sub-steps (capped at 2)
//
// A short single-action task ("read main.go") scores ~1; a multi-file
// refactor ("update auth.go and add tests/auth_test.go then run go test")
// hits the 6+ threshold and triggers Plan-First when Mode=auto.
func ComplexityScore(task string) int {
	if strings.TrimSpace(task) == "" {
		return 0
	}
	lower := strings.ToLower(task)

	// Action verbs (cap 5).
	verbs := countDistinctVerbs(lower, 5)

	// Concrete file artefacts (cap 3).
	files := countDistinctMatches(fileExtensionRE.FindAllString(task, -1), 3)
	files += countDistinctMatches(dockerfilesRE.FindAllString(task, -1), 3-files)

	// Sequencer tokens (cap 2).
	seqs := 0
	for _, s := range sequencers {
		if strings.Contains(lower, s) {
			seqs++
			if seqs >= 2 {
				break
			}
		}
	}

	score := verbs + files + seqs
	if score > 10 {
		score = 10
	}
	if score < 0 {
		score = 0
	}
	return score
}

// ShouldPlanFirst decides whether the orchestrator should synthesize a
// structured plan before dispatching, given the user-configured mode and
// the live task complexity.
//
//   - "off"    → never
//   - "always" → always
//   - "auto"   → ComplexityScore(task) >= cfg.ComplexityThreshold
//
// Unknown modes fall through to "auto" so a misconfigured env doesn't
// silently disable planning.
func ShouldPlanFirst(cfg PlanFirstConfig, task string) bool {
	switch strings.ToLower(strings.TrimSpace(cfg.Mode)) {
	case "off":
		return false
	case "always":
		return true
	default: // "auto" or unknown → conservative auto behaviour
		return ComplexityScore(task) >= cfg.ComplexityThreshold
	}
}

// countDistinctVerbs counts unique action verbs in lower (already
// lower-cased), up to cap. Word boundaries are enforced via Fields,
// which keeps the scorer simple and Unicode-safe.
func countDistinctVerbs(lower string, cap int) int {
	seen := make(map[string]struct{})
	for _, raw := range strings.Fields(lower) {
		w := strings.Trim(raw, ".,;:!?()[]{}\"'`")
		if _, ok := actionVerbs[w]; ok {
			seen[w] = struct{}{}
			if len(seen) >= cap {
				return cap
			}
		}
	}
	return len(seen)
}

// countDistinctMatches dedupes a slice and returns count up to cap.
func countDistinctMatches(matches []string, cap int) int {
	if cap <= 0 || len(matches) == 0 {
		return 0
	}
	seen := make(map[string]struct{})
	for _, m := range matches {
		seen[strings.ToLower(m)] = struct{}{}
		if len(seen) >= cap {
			return cap
		}
	}
	return len(seen)
}
