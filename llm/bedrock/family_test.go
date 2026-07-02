package bedrock

import (
	"errors"
	"os"
	"strings"
	"testing"

	bedrocktypes "github.com/aws/aws-sdk-go-v2/service/bedrock/types"
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

		// Unknown / non-Anthropic / non-OpenAI prefixes route to Converse —
		// one schema covers Llama, Nova, Mistral, Cohere, AI21, DeepSeek,
		// Stability, and any future provider.
		{"meta llama defaults to converse", "", "meta.llama3-70b-instruct-v1:0", familyConverse},
		{"amazon nova defaults to converse", "", "amazon.nova-pro-v1:0", familyConverse},
		{"mistral defaults to converse", "", "mistral.mistral-large-2407-v1:0", familyConverse},
		{"cohere defaults to converse", "", "cohere.command-r-plus-v1:0", familyConverse},
		{"ai21 defaults to converse", "", "ai21.jamba-1-5-large-v1:0", familyConverse},
		{"deepseek defaults to converse", "", "us.deepseek.r1-v1:0", familyConverse},

		// Env override takes precedence
		{"env override openai", "openai", "anthropic.claude-3-5-sonnet-20241022-v2:0", familyOpenAI},
		{"env override gpt alias", "gpt", "anthropic.claude-3-5-sonnet-20241022-v2:0", familyOpenAI},
		{"env override anthropic", "anthropic", "openai.gpt-oss-120b-1:0", familyAnthropic},
		{"env override claude alias", "claude", "openai.gpt-oss-120b-1:0", familyAnthropic},
		{"env override case insensitive", "OpenAI", "anthropic.claude-3-5-sonnet-20241022-v2:0", familyOpenAI},
		{"env override converse", "converse", "anthropic.claude-3-5-sonnet-20241022-v2:0", familyConverse},
		{"env override auto alias", "auto", "openai.gpt-oss-120b-1:0", familyConverse},
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

// TestSupportsOnDemand pins the only listing-side gate we keep:
// foundation models that don't expose ON_DEMAND aren't directly invokable
// by their bare ID and would fail with ValidationException at first call.
// We rely on the matching ListInferenceProfiles entry to surface them.
func TestSupportsOnDemand(t *testing.T) {
	cases := []struct {
		name string
		in   []bedrocktypes.InferenceType
		want bool
	}{
		{"empty list", nil, false},
		{"only provisioned", []bedrocktypes.InferenceType{bedrocktypes.InferenceTypeProvisioned}, false},
		{"on-demand only", []bedrocktypes.InferenceType{bedrocktypes.InferenceTypeOnDemand}, true},
		{"both", []bedrocktypes.InferenceType{
			bedrocktypes.InferenceTypeOnDemand,
			bedrocktypes.InferenceTypeProvisioned,
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := supportsOnDemand(tc.in); got != tc.want {
				t.Errorf("supportsOnDemand(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// AWS answers the legacy InvokeModel surface with a 400
// ValidationException when a model is only served under the
// Claude-in-Amazon-Bedrock data-retention agreement (Fable 5 requires
// 30-day retention; the default retention mode is rejected). The raw
// message doesn't say what to do about it — the wrapper must point at the
// Mantle endpoint and the console opt-in.
func TestWrapBedrockDataRetentionError(t *testing.T) {
	base := errors.New("operation error Bedrock Runtime: InvokeModel, " +
		"https response error StatusCode: 400, ValidationException: " +
		"Data retention mode 'default' is not available for this model.")
	err := wrapBedrockInferenceProfileError("us.anthropic.claude-fable-5", base)
	if err == nil {
		t.Fatal("expected a wrapped error, got nil")
	}
	for _, want := range []string{
		"Claude in Amazon Bedrock",
		"BEDROCK_ANTHROPIC_ENDPOINT",
		"us.anthropic.claude-fable-5",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("wrapped error %q missing %q", err.Error(), want)
		}
	}
	if !errors.Is(err, base) {
		t.Error("wrapped error must preserve the original via %w")
	}

	// Unrelated errors pass through untouched (same value, no wrapping).
	other := errors.New("ThrottlingException: too many requests")
	got := wrapBedrockInferenceProfileError("anthropic.claude-fable-5", other)
	if !errors.Is(got, other) || got.Error() != other.Error() {
		t.Errorf("unrelated error must pass through untouched, got %v", got)
	}
}

func TestSuggestInferenceProfilePrefix(t *testing.T) {
	cases := []struct {
		model string
		// substrings every suggestion must contain so users see a copy-paste
		// hint regardless of the exact format.
		mustContain []string
	}{
		{"anthropic.claude-3-7-sonnet-20250219-v1:0",
			[]string{"global.anthropic.claude-3-7-sonnet-20250219-v1:0",
				"us.anthropic.claude-3-7-sonnet-20250219-v1:0"}},
		{"meta.llama3-70b-instruct-v1:0",
			[]string{"global.meta.llama3-70b-instruct-v1:0"}},
		// Already-prefixed IDs should not get a re-suggestion that wraps
		// them again.
		{"global.anthropic.claude-sonnet-4-5-20250929-v1:0",
			[]string{"different inference profile"}},
		{"", []string{"inference profile"}},
	}
	for _, tc := range cases {
		got := suggestInferenceProfilePrefix(tc.model)
		for _, want := range tc.mustContain {
			if !strings.Contains(got, want) {
				t.Errorf("suggestInferenceProfilePrefix(%q) = %q, missing %q", tc.model, got, want)
			}
		}
	}
}
