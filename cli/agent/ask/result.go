/*
 * Result formatters for the @ask tool.
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 *
 * The tool result is a plain string fed back to the LLM as a tool_result (and
 * also rendered to the user). We emit a human-readable summary followed by a
 * canonical machine-parseable JSON block, so weaker text-mode models get prose
 * while strong models can parse the exact selections. These strings are an
 * LLM-facing contract and are kept in English for stability, matching the other
 * builtin tools (@park, @coder, web_fetch).
 */
package ask

import (
	"encoding/json"
	"fmt"
	"strings"
)

// answersJSON marshals answers into the canonical {"answers":[...]} block.
func answersJSON(answers []Answer) string {
	if answers == nil {
		answers = []Answer{}
	}
	b, err := json.Marshal(map[string]interface{}{"answers": answers})
	if err != nil {
		return `{"answers":[]}`
	}
	return string(b)
}

// summaryLine renders one answer as "Header → choice, choice" / "Other: text".
func summaryLine(a Answer) string {
	switch {
	case a.Other != "":
		return fmt.Sprintf("- %s → Other: %q", a.Header, a.Other)
	case len(a.Selected) > 0:
		return fmt.Sprintf("- %s → %s", a.Header, strings.Join(a.Selected, ", "))
	default:
		return fmt.Sprintf("- %s → (no selection)", a.Header)
	}
}

// FormatResult builds the tool result for a completed prompt.
func FormatResult(answers []Answer) string {
	var b strings.Builder
	b.WriteString("User answered:\n")
	for _, a := range answers {
		b.WriteString(summaryLine(a))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	b.WriteString(answersJSON(answers))
	return b.String()
}

// FallbackResult is returned when there is no interactive terminal (unattended
// gateway/daemon, piped one-shot). It auto-selects the first option of every
// question — the conventional default — and says so explicitly, then emits the
// same JSON block so the model proceeds deterministically without blocking.
func FallbackResult(qs []Question) string {
	answers := DefaultAnswers(qs)
	var b strings.Builder
	b.WriteString("No interactive UI available (unattended/non-TTY). Auto-selected the first option per question:\n")
	for _, a := range answers {
		b.WriteString(summaryLine(a))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	b.WriteString(answersJSON(answers))
	return b.String()
}

// DefaultAnswers picks the first option of each question. Used by the
// non-interactive fallback and as a safe default elsewhere.
func DefaultAnswers(qs []Question) []Answer {
	answers := make([]Answer, 0, len(qs))
	for _, q := range qs {
		sel := []string{}
		if len(q.Options) > 0 {
			sel = []string{q.Options[0].Label}
		}
		answers = append(answers, Answer{Header: q.Header, Selected: sel})
	}
	return answers
}

// CanceledResult is returned when the user dismisses the prompt (Esc/Ctrl+C)
// without answering. It is NOT an error — the model should continue with
// reasonable defaults or ask again in text.
func CanceledResult() string {
	return "User canceled the question prompt without answering. " +
		"Proceed with reasonable defaults or ask again in plain text.\n\n" +
		`{"answers":[],"canceled":true}`
}

// ErrorResult wraps a parse/validation failure as a tool result the model can
// learn from and retry.
func ErrorResult(err error) string {
	return fmt.Sprintf("ask_user error: %v", err)
}
