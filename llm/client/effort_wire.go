/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package client

import "strings"

// Per-turn effort on the wire, one mapping for every provider.
//
// The canonical level (skill frontmatter, /effort, a slash command's
// routing) has to reach the request or it is only a preference. Each
// family names the field differently — output_config.effort on Anthropic,
// reasoning_effort on the OpenAI-compatible chat schema, reasoning.effort
// on the Responses and OpenRouter schemas, a thinking budget on Gemini —
// so the mapping lives here and every provider assigns the value into its
// own body. Returning the value (instead of mutating a body map) keeps
// the wire shape owned by the provider that knows it.
//
// Every helper returns a zero value when nothing should be sent, which is
// also what an unset hint means: the provider's own default then applies,
// exactly as before effort was wired.

// SupportsOpenAIReasoningEffort reports whether a model on the
// OpenAI-compatible chat schema accepts reasoning_effort. Shared by the
// direct OpenAI client, the Responses client, Copilot, GitHub Models and
// the Bedrock OpenAI family, which all speak the same dialect and used to
// carry their own copy of this list (or none at all).
func SupportsOpenAIReasoningEffort(model string) bool {
	m := strings.ToLower(model)
	return strings.HasPrefix(m, "o1") ||
		strings.HasPrefix(m, "o3") ||
		strings.HasPrefix(m, "o4") ||
		strings.HasPrefix(m, "gpt-5") ||
		strings.Contains(m, "gpt-oss") ||
		strings.Contains(m, "-reasoning")
}

// OpenAIReasoningEffort is the value for reasoning_effort (or the effort
// field of the Responses/OpenRouter reasoning object) on a given model.
// Empty when the model does not take the field or the hint is unset.
func OpenAIReasoningEffort(model string, e SkillEffort) string {
	if !SupportsOpenAIReasoningEffort(model) {
		return ""
	}
	return ReasoningEffortForOpenAI(e)
}

// XAIReasoningEffort is the value for grok's reasoning_effort, which
// accepts only the two ends of the scale. Anything at or above high maps
// to high; low and medium map to low, so a cheap turn stays cheap.
func XAIReasoningEffort(model string, e SkillEffort) string {
	m := strings.ToLower(model)
	if !strings.Contains(m, "mini") && !strings.Contains(m, "reasoning") {
		return ""
	}
	switch e {
	case EffortLow, EffortMedium:
		return "low"
	case EffortHigh, EffortXHigh, EffortMax:
		return "high"
	}
	return ""
}

// GeminiThinkingLevel is the value for
// generationConfig.thinkingConfig.thinkingLevel. Gemini takes a named
// level, not a token budget, and rejects a request that carries both the
// level and the legacy budget — so this is the only thinking knob the
// Gemini path sends. Empty means send nothing and let the model decide,
// which is Gemini's own default.
func GeminiThinkingLevel(e SkillEffort) string {
	switch e {
	case EffortLow:
		return "low"
	case EffortMedium:
		return "medium"
	case EffortHigh, EffortXHigh, EffortMax:
		return "high"
	}
	return ""
}
