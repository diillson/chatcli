/*
 * ChatCLI - Per-request skill hints
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Carries per-turn hints from auto-activated skills (frontmatter `effort:`
 * and `model:`) through context.Context so that provider clients can opt
 * into extended thinking / reasoning_effort for a single SendPrompt call
 * without changing the LLMClient interface.
 */
package client

import "context"

// SkillEffort normalizes the `effort:` frontmatter field to a small set of
// canonical levels. Unknown values fall through to EffortUnset.
type SkillEffort string

const (
	EffortUnset  SkillEffort = ""
	EffortLow    SkillEffort = "low"
	EffortMedium SkillEffort = "medium"
	EffortHigh   SkillEffort = "high"
	EffortXHigh  SkillEffort = "xhigh"
	EffortMax    SkillEffort = "max"
)

// Normalize coerces raw frontmatter values ("Low", "HIGH", "maximum", etc.)
// into the canonical set. Returns EffortUnset for anything unrecognized.
func NormalizeEffort(raw string) SkillEffort {
	switch raw {
	case "low", "Low", "LOW":
		return EffortLow
	case "medium", "Medium", "MEDIUM", "med":
		return EffortMedium
	case "high", "High", "HIGH":
		return EffortHigh
	case "xhigh", "XHigh", "XHIGH", "x-high", "X-High", "extra-high", "extra_high":
		return EffortXHigh
	case "max", "Max", "MAX", "maximum", "Maximum", "MAXIMUM":
		return EffortMax
	}
	return EffortUnset
}

type effortCtxKey struct{}

// WithEffortHint returns a child context carrying the given effort level.
// Provider clients call EffortFromContext inside SendPrompt to read it.
func WithEffortHint(ctx context.Context, effort SkillEffort) context.Context {
	if effort == EffortUnset {
		return ctx
	}
	return context.WithValue(ctx, effortCtxKey{}, effort)
}

// EffortFromContext returns the effort hint attached to ctx, if any.
func EffortFromContext(ctx context.Context) SkillEffort {
	if ctx == nil {
		return EffortUnset
	}
	if v, ok := ctx.Value(effortCtxKey{}).(SkillEffort); ok {
		return v
	}
	return EffortUnset
}

// ThinkingBudgetForEffort maps a canonical effort level to a budget_tokens
// value for Anthropic extended thinking. The mapping is deliberately
// conservative so that most turns stay in the cheap tier.
//
// Returns 0 when extended thinking should NOT be enabled (EffortUnset/Low).
func ThinkingBudgetForEffort(e SkillEffort) int {
	switch e {
	case EffortMedium:
		return 4096
	case EffortHigh:
		return 16384
	case EffortXHigh:
		return 24576
	case EffortMax:
		return 32768
	}
	return 0
}

// ReasoningEffortForOpenAI maps the canonical effort level to the string
// accepted by OpenAI's `reasoning.effort` field ("low", "medium", "high").
// Returns "" when the hint should NOT be sent.
func ReasoningEffortForOpenAI(e SkillEffort) string {
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

// AnthropicEffortLevel maps the canonical level to the string Anthropic's
// output_config.effort accepts. Effort is the lever that decides how much
// the model thinks and spends; the thinking block only says whether it
// thinks at all, so a client that sends the block alone collapses every
// level into one request.
//
// Returns "" when nothing should be sent, which is also what an unset hint
// means: the provider's own default (high) then applies, unchanged from
// the behavior before effort was wired.
func AnthropicEffortLevel(e SkillEffort) string {
	switch e {
	case EffortLow:
		return "low"
	case EffortMedium:
		return "medium"
	case EffortHigh:
		return "high"
	case EffortXHigh:
		return "xhigh"
	case EffortMax:
		return "max"
	}
	return ""
}

// AnthropicOutputConfig builds the output_config request block for one
// effort level. Empty when the level carries nothing to send, so callers
// can assign it without a nil check.
func AnthropicOutputConfig(e SkillEffort) map[string]string {
	level := AnthropicEffortLevel(e)
	if level == "" {
		return nil
	}
	return map[string]string{"effort": level}
}
