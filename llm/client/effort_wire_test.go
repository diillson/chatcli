package client

import "testing"

func TestNormalizeEffortAcceptsXHigh(t *testing.T) {
	for _, raw := range []string{"xhigh", "XHIGH", "x-high", "extra-high", "extra_high"} {
		if got := NormalizeEffort(raw); got != EffortXHigh {
			t.Errorf("NormalizeEffort(%q) = %q, want xhigh", raw, got)
		}
	}
	if got := NormalizeEffort("nonsense"); got != EffortUnset {
		t.Errorf("unknown level must stay unset, got %q", got)
	}
}

// The level has to survive the trip to the wire on every family, or the
// whole scale collapses into one request.
func TestEffortReachesEveryFamily(t *testing.T) {
	cases := []struct {
		effort    SkillEffort
		anthropic string
		openai    string
		xai       string
		gemini    string
	}{
		{EffortUnset, "", "", "", ""},
		{EffortLow, "low", "low", "low", "low"},
		{EffortMedium, "medium", "medium", "low", "medium"},
		{EffortHigh, "high", "high", "high", "high"},
		{EffortXHigh, "xhigh", "high", "high", "high"},
		{EffortMax, "max", "high", "high", "high"},
	}
	for _, tc := range cases {
		t.Run(string(tc.effort), func(t *testing.T) {
			if got := AnthropicEffortLevel(tc.effort); got != tc.anthropic {
				t.Errorf("anthropic = %q, want %q", got, tc.anthropic)
			}
			if got := OpenAIReasoningEffort("gpt-5.6-terra", tc.effort); got != tc.openai {
				t.Errorf("openai = %q, want %q", got, tc.openai)
			}
			if got := XAIReasoningEffort("grok-4-mini", tc.effort); got != tc.xai {
				t.Errorf("xai = %q, want %q", got, tc.xai)
			}
			if got := GeminiThinkingLevel(tc.effort); got != tc.gemini {
				t.Errorf("gemini = %q, want %q", got, tc.gemini)
			}
		})
	}
}

func TestAnthropicOutputConfigShape(t *testing.T) {
	if cfg := AnthropicOutputConfig(EffortUnset); cfg != nil {
		t.Errorf("unset must send nothing, got %+v", cfg)
	}
	cfg := AnthropicOutputConfig(EffortXHigh)
	if cfg == nil || cfg["effort"] != "xhigh" {
		t.Fatalf("output_config = %+v", cfg)
	}
	if len(cfg) != 1 {
		t.Errorf("output_config must carry only the effort field: %+v", cfg)
	}
}

func TestOpenAIEffortOnlyForReasoningModels(t *testing.T) {
	if got := OpenAIReasoningEffort("gpt-4.1", EffortHigh); got != "" {
		t.Errorf("a non-reasoning model must not carry the field, got %q", got)
	}
	for _, m := range []string{"o1-preview", "o3-mini", "o4", "gpt-5.6-terra", "gpt-oss-120b", "custom-reasoning"} {
		if !SupportsOpenAIReasoningEffort(m) {
			t.Errorf("%s should take reasoning_effort", m)
		}
	}
}

// Grok exposes only the two ends of the scale, so the middle maps down:
// a turn asked to be cheap must not become expensive on one provider.
func TestXAIEffortOnlyForReasoningModels(t *testing.T) {
	if got := XAIReasoningEffort("grok-4", EffortHigh); got != "" {
		t.Errorf("a non-reasoning grok must not carry the field, got %q", got)
	}
}

func TestThinkingBudgetOrderedByLevel(t *testing.T) {
	low := ThinkingBudgetForEffort(EffortMedium)
	high := ThinkingBudgetForEffort(EffortHigh)
	xhigh := ThinkingBudgetForEffort(EffortXHigh)
	max := ThinkingBudgetForEffort(EffortMax)
	if !(low < high && high < xhigh && xhigh < max) {
		t.Errorf("budgets must increase with the level: %d %d %d %d", low, high, xhigh, max)
	}
	if ThinkingBudgetForEffort(EffortLow) != 0 || ThinkingBudgetForEffort(EffortUnset) != 0 {
		t.Error("low and unset must not enable budgeted thinking")
	}
}
