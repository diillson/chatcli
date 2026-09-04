package claudeai

import (
	"context"
	"testing"

	"github.com/diillson/chatcli/llm/client"
)

// The level is what decides how much the model thinks and spends. Before
// this, every level produced the same request and the provider's default
// applied, so low and max were indistinguishable on the wire.
func TestEffortTravelsInOutputConfig(t *testing.T) {
	cases := []struct {
		model     string
		effort    client.SkillEffort
		wantLevel string
		wantThink string // "adaptive", "budgeted" or ""
	}{
		{"claude-opus-5", client.EffortLow, "low", "adaptive"},
		{"claude-opus-5", client.EffortXHigh, "xhigh", "adaptive"},
		{"claude-opus-5", client.EffortMax, "max", "adaptive"},
		{"claude-fable-5-1", client.EffortHigh, "high", "adaptive"},
		// Budgeted generation: no output_config, the budget carries the level.
		{"claude-sonnet-4-5", client.EffortHigh, "", "budgeted"},
		// No hint: nothing is sent and the provider default applies.
		{"claude-opus-5", client.EffortUnset, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.model+"/"+string(tc.effort), func(t *testing.T) {
			body := map[string]interface{}{"max_tokens": 4096}
			ctx := context.Background()
			if tc.effort != client.EffortUnset {
				ctx = client.WithEffortHint(ctx, tc.effort)
			}
			applyThinkingForEffort(body, tc.model, ctx)

			cfg, hasCfg := body["output_config"].(map[string]string)
			if tc.wantLevel == "" {
				if hasCfg {
					t.Fatalf("output_config must not be sent: %+v", cfg)
				}
			} else {
				if !hasCfg || cfg["effort"] != tc.wantLevel {
					t.Fatalf("output_config = %+v, want effort %q", body["output_config"], tc.wantLevel)
				}
			}

			think, hasThink := body["thinking"].(map[string]interface{})
			switch tc.wantThink {
			case "":
				if hasThink {
					t.Fatalf("thinking must not be sent: %+v", think)
				}
			case "adaptive":
				if !hasThink || think["type"] != "adaptive" {
					t.Fatalf("thinking = %+v, want adaptive", body["thinking"])
				}
			case "budgeted":
				if !hasThink || think["type"] != "enabled" {
					t.Fatalf("thinking = %+v, want budgeted", body["thinking"])
				}
			}
		})
	}
}

// The catalog is the single source of truth for a registered model; the
// name match survives only for ids the catalog does not know, so an
// unlisted snapshot keeps the thinking it has today.
func TestExtendedThinkingComesFromTheCatalog(t *testing.T) {
	if !supportsExtendedThinking("claude-sonnet-4-5") {
		t.Error("a registered budgeted model must be recognized")
	}
	if supportsExtendedThinking("claude-opus-5") {
		t.Error("a registered adaptive model must not take a token budget")
	}
	if !supportsExtendedThinking("claude-sonnet-4-5-20250929") {
		t.Error("an unlisted snapshot must fall back to the name match")
	}
	if supportsExtendedThinking("gpt-5.6-terra") {
		t.Error("a foreign model must not be treated as thinking-capable")
	}
}
