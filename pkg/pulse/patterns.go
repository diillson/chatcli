/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package pulse

// The seven harness patterns, by the names the project documents them under
// (docs/AGENT_PATTERNS.md). Each is one node on the live dashboard; the
// packages that implement them live in different layers, so the names are
// shared here.
const (
	PatternReAct        = "react"          // #1 reason + act loop
	PatternPlanAndSolve = "plan-and-solve" // #2 plan-first / ReWOO
	PatternReflexion    = "reflexion"      // #3 lessons from failures
	PatternRAGHyDE      = "rag-hyde"       // #4 hypothetical-document retrieval
	PatternSelfRefine   = "self-refine"    // #5 critique and rewrite
	PatternCoVe         = "cove"           // #6 chain of verification
	PatternReasoning    = "reasoning"      // #7 cross-provider thinking effort
)

// Outcomes of the background half of Reflexion, shared by the durable queue
// worker and the legacy detached path.
const (
	OutcomeLessonSaved = "lesson saved"
	OutcomeNoLesson    = "no lesson"
	OutcomeRetrying    = "retrying"
	OutcomeDeadLetter  = "dead letter"
)

// BeginPattern opens the span of a harness pattern that actually fired — its
// guards passed and it is about to do work — under the run it serves.
func BeginPattern(name, parent string) *Span {
	return Begin(KindPattern, name, parent)
}

// Outcome closes a pattern span with what it concluded. The outcome is a
// short fixed label chosen by the caller ("rewrote draft", "lesson saved"),
// never model output; it shows as the state of the pattern node.
func (s *Span) Outcome(status, outcome string) {
	s.With("state", outcome).End(status)
}
