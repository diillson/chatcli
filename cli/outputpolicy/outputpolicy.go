/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

// Package outputpolicy reduces the tokens a model *generates* (the output side
// of the bill, complementary to the input/context compression in cli/compress).
//
// Two keyless levers, both standard practice:
//
//   - Verbosity steering: a static, cache-friendly directive injected into the
//     system prompt that tells the model to drop preamble, restatement and
//     ceremony and lead with the answer/action. Levels: full (off), concise,
//     minimal.
//   - Effort right-sizing: a keyless complexity classifier so trivial requests
//     don't burn extended-thinking/reasoning tokens. It only ever *lowers*
//     effort for clearly-trivial prompts; it never forces more thinking, so it
//     cannot degrade a hard task.
//
// The package is a leaf (standard library only) so it can be imported by the
// agent loop and the chat pipeline without cycles. The directives are
// model-facing instructions and are intentionally hard-coded in English (the
// repo convention: models follow English directives most reliably).
package outputpolicy

import "strings"

// Verbosity selects how terse the model should be.
type Verbosity int

const (
	// VerbosityFull applies no steering — the model's natural verbosity.
	VerbosityFull Verbosity = iota
	// VerbosityConcise drops ceremony and restatement while keeping substance.
	// This is the recommended default.
	VerbosityConcise
	// VerbosityMinimal answers in the fewest correct tokens.
	VerbosityMinimal
)

// String renders the level for /config and logs.
func (v Verbosity) String() string {
	switch v {
	case VerbosityFull:
		return "full"
	case VerbosityConcise:
		return "concise"
	case VerbosityMinimal:
		return "minimal"
	default:
		return "unknown"
	}
}

// ParseVerbosity maps a config/env string to a Verbosity. Unknown values fall
// back to VerbosityConcise (the recommended default) with ok=false.
func ParseVerbosity(s string) (Verbosity, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "full", "off", "verbose", "none":
		return VerbosityFull, true
	case "concise", "brief", "default", "":
		return VerbosityConcise, true
	case "minimal", "terse", "min":
		return VerbosityMinimal, true
	default:
		return VerbosityConcise, false
	}
}

const conciseDirective = `## OUTPUT STYLE
Be concise and direct. Lead with the answer or the action — no preamble, no
restating the request, no "Sure, here's…" openers, no closing summaries unless
asked. Do not re-explain or re-paste code you just wrote. Use the minimum prose
needed to be correct and clear. Don't over-deliberate on routine requests.`

const minimalDirective = `## OUTPUT STYLE
Minimal output. Answer in the fewest tokens that are still correct. No preamble,
no summary, no restating the question, no explanation unless explicitly asked.
Prefer a single sentence or terse bullet points. For code, output only the code
plus at most a one-line note when essential. Never restate code you just wrote.`

// Directive returns the system-prompt steering text for v, or "" for
// VerbosityFull (no steering). The strings are static so the block stays in the
// provider's cached prefix.
func (v Verbosity) Directive() string {
	switch v {
	case VerbosityConcise:
		return conciseDirective
	case VerbosityMinimal:
		return minimalDirective
	default:
		return ""
	}
}

// Complexity is the classifier's verdict about a user prompt.
type Complexity int

const (
	// ComplexityNormal is the default — no effort change.
	ComplexityNormal Complexity = iota
	// ComplexityTrivial is a short, conversational lookup that doesn't need
	// extended thinking.
	ComplexityTrivial
	// ComplexityComplex is a multi-step / design / debugging task.
	ComplexityComplex
)

// String renders the verdict.
func (c Complexity) String() string {
	switch c {
	case ComplexityTrivial:
		return "trivial"
	case ComplexityComplex:
		return "complex"
	default:
		return "normal"
	}
}

// trivialMaxLen is the byte length under which a prompt is a candidate for
// "trivial" (above it, there's usually enough specificity to warrant thinking).
const trivialMaxLen = 140

// complexSignals mark a prompt as needing real reasoning. Lower-cased substring
// match; deliberately conservative (false negatives just leave effort at the
// default).
var complexSignals = []string{
	"refactor", "architect", "design ", "redesign", "debug", "root cause",
	"optim", "migrate", "migration", "implement", "rewrite",
	"why does", "why is", "trade-off", "tradeoff", "compare", "across the",
	"step by step", "plan ", "investigate", "diagnos", "concurren", "race condition",
	"refatorar", "arquitetura", "depurar", "otimizar", "migrar", "implementar",
	"investigar", "passo a passo", "causa raiz", "concorrência",
}

// trivialLeads are conversational openers typical of quick lookups.
var trivialLeads = []string{
	"what is", "what's", "who is", "when ", "where ", "list ", "show ",
	"define ", "meaning of", "spell", "translate",
	"o que é", "o que e", "qual ", "quais ", "liste ", "mostre ", "defina ",
	"significado de", "traduz",
}

// Classify returns a keyless complexity verdict for a user prompt. It is used
// only to *lower* effort for trivial prompts; ComplexityComplex/Normal leave
// effort untouched, so misclassification never reduces thinking on a hard task.
func Classify(prompt string) Complexity {
	q := strings.ToLower(strings.TrimSpace(prompt))
	if q == "" {
		return ComplexityNormal
	}
	for _, s := range complexSignals {
		if strings.Contains(q, s) {
			return ComplexityComplex
		}
	}
	// Code fences / tool markup / multiple sentences imply non-trivial work.
	if strings.Contains(q, "```") || strings.Contains(q, "<tool_call") {
		return ComplexityComplex
	}
	if len(q) > trivialMaxLen {
		return ComplexityNormal
	}
	// Short and not flagged complex: trivial if it opens like a quick lookup or
	// is a single short clause.
	for _, lead := range trivialLeads {
		if strings.HasPrefix(q, lead) {
			return ComplexityTrivial
		}
	}
	if len(q) <= 60 && sentenceCount(q) <= 1 {
		return ComplexityTrivial
	}
	return ComplexityNormal
}

// sentenceCount counts terminal punctuation as a cheap proxy for multi-step.
func sentenceCount(s string) int {
	n := 0
	for _, r := range s {
		if r == '.' || r == '?' || r == '!' || r == '\n' {
			n++
		}
	}
	if n == 0 {
		return 1
	}
	return n
}
