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
// direct OpenAI client, the Responses client, Copilot and
// the Bedrock OpenAI family, which all speak the same dialect and used to
// carry their own copy of this list (or none at all). The GPT-6 family
// (Astra, Sol, Luna, 6.1 Sol) reasons too and was missing, so effort hints
// never reached it; every value ReasoningEffortForOpenAI emits (low,
// medium, high) is valid there — 6.1 Sol dropped only none/minimal.
func SupportsOpenAIReasoningEffort(model string) bool {
	m := strings.ToLower(model)
	return strings.HasPrefix(m, "o1") ||
		strings.HasPrefix(m, "o3") ||
		strings.HasPrefix(m, "o4") ||
		strings.HasPrefix(m, "gpt-5") ||
		strings.HasPrefix(m, "gpt-6") ||
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

// openAIEffortCeiling is the highest reasoning effort a first-party OpenAI
// model accepts, as the API itself answers (probed Nov 19 2026 by sending
// each level to /v1/responses: an unsupported one is a 400 that names the
// accepted set). The GPT-6 family and the GPT-5.6 tiers take xhigh and max;
// GPT-5.2 through 5.5, their pro/mini siblings and 5.3-codex stop at xhigh;
// GPT-5, GPT-5.1 and the o-series stop at high. An id this table does not
// know keeps the conservative high.
func openAIEffortCeiling(model string) SkillEffort {
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.HasPrefix(m, "gpt-5.6"):
		return EffortMax
	case strings.HasPrefix(m, "gpt-5.2"), strings.HasPrefix(m, "gpt-5.3"),
		strings.HasPrefix(m, "gpt-5.4"), strings.HasPrefix(m, "gpt-5.5"):
		return EffortXHigh
	case strings.HasPrefix(m, "gpt-5"):
		return EffortHigh
	}
	if g := gptGeneration(m); g >= 6 {
		return EffortMax
	}
	return EffortHigh
}

// gptGeneration reads the major version off a "gpt-<n>..." id; 0 when the
// id is not one.
func gptGeneration(m string) int {
	rest, ok := strings.CutPrefix(m, "gpt-")
	if !ok {
		return 0
	}
	n := 0
	for _, r := range rest {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// OpenAIModelReasoningEffort is the reasoning effort to send to OpenAI's
// own API for a model: the requested level, lowered to the highest one the
// model accepts. Unlike OpenAIReasoningEffort, which stops at high for
// every model, it lets xhigh and max through where the API takes them —
// before, both were sent as high and a turn asking for the top level ran
// two levels below it on GPT-6. Empty when the model does not take the
// field or the hint is unset.
func OpenAIModelReasoningEffort(model string, e SkillEffort) string {
	if !SupportsOpenAIReasoningEffort(model) {
		return ""
	}
	switch e {
	case EffortXHigh, EffortMax:
	default:
		return ReasoningEffortForOpenAI(e)
	}
	switch openAIEffortCeiling(model) {
	case EffortMax:
		if e == EffortMax {
			return "max"
		}
		return "xhigh"
	case EffortXHigh:
		return "xhigh"
	}
	return "high"
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
