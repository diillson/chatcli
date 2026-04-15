package bedrock

import (
	"os"
	"testing"
)

func TestResolveFamily(t *testing.T) {
	tests := []struct {
		name        string
		envOverride string
		model       string
		want        modelFamily
	}{
		// Auto-detection by model id prefix
		{"anthropic direct base id", "", "anthropic.claude-3-5-sonnet-20241022-v2:0", familyAnthropic},
		{"anthropic global profile", "", "global.anthropic.claude-sonnet-4-5-20250929-v1:0", familyAnthropic},
		{"anthropic us profile", "", "us.anthropic.claude-opus-4-20250514-v1:0", familyAnthropic},
		{"openai gpt-oss 120b", "", "openai.gpt-oss-120b-1:0", familyOpenAI},
		{"openai gpt-oss 20b", "", "openai.gpt-oss-20b-1:0", familyOpenAI},
		{"openai via regional profile", "", "us.openai.gpt-oss-120b-1:0", familyOpenAI},

		// Unknown prefix falls back to default (anthropic)
		{"unknown prefix defaults to anthropic", "", "meta.llama3-70b-instruct-v1:0", familyAnthropic},

		// Env override takes precedence
		{"env override openai", "openai", "anthropic.claude-3-5-sonnet-20241022-v2:0", familyOpenAI},
		{"env override gpt alias", "gpt", "anthropic.claude-3-5-sonnet-20241022-v2:0", familyOpenAI},
		{"env override anthropic", "anthropic", "openai.gpt-oss-120b-1:0", familyAnthropic},
		{"env override claude alias", "claude", "openai.gpt-oss-120b-1:0", familyAnthropic},
		{"env override case insensitive", "OpenAI", "anthropic.claude-3-5-sonnet-20241022-v2:0", familyOpenAI},
		{"env invalid value ignored", "bogus", "openai.gpt-oss-120b-1:0", familyOpenAI},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			prev, had := os.LookupEnv("BEDROCK_PROVIDER")
			t.Cleanup(func() {
				if had {
					os.Setenv("BEDROCK_PROVIDER", prev)
				} else {
					os.Unsetenv("BEDROCK_PROVIDER")
				}
			})
			if tc.envOverride == "" {
				os.Unsetenv("BEDROCK_PROVIDER")
			} else {
				os.Setenv("BEDROCK_PROVIDER", tc.envOverride)
			}
			if got := resolveFamily(tc.model); got != tc.want {
				t.Fatalf("resolveFamily(%q) with BEDROCK_PROVIDER=%q = %q, want %q",
					tc.model, tc.envOverride, got, tc.want)
			}
		})
	}
}

func TestIsSupportedBedrockFamily(t *testing.T) {
	cases := map[string]bool{
		"anthropic.claude-3-5-sonnet-20241022-v2:0":       true,
		"global.anthropic.claude-sonnet-4-5-20250929-v1:0": true,
		"openai.gpt-oss-120b-1:0":                          true,
		"us.openai.gpt-oss-120b-1:0":                       true,
		"meta.llama3-70b-instruct-v1:0":                    false,
		"amazon.nova-pro-v1:0":                             false,
		"mistral.mistral-large-2407-v1:0":                  false,
		"":                                                 false,
	}
	for id, want := range cases {
		if got := isSupportedBedrockFamily(id); got != want {
			t.Errorf("isSupportedBedrockFamily(%q) = %v, want %v", id, got, want)
		}
	}
}
