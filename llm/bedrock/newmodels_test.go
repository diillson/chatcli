/*
 * ChatCLI - Tests for Claude Fable 5 / new-generation model support on Bedrock
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package bedrock

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/models"
)

// Compile-time guard: BedrockClient must stay a comparable type. Floor 13
// (apidiff) treats losing comparability as a breaking API change — adding
// a field with a non-comparable type (slice, map, aws.Config, …) breaks
// any downstream == comparison of client values. Using the type as a map
// key only compiles while it remains comparable.
var _ = map[BedrockClient]struct{}(nil)

// Claude models on Bedrock always speak the Anthropic Messages schema —
// routing a bare "claude-*" ID (no "anthropic." segment) through Converse
// silently drops cache_control markers and extended-thinking knobs. These
// cases pin the content-based fallback in resolveFamily.
func TestResolveFamilyBareClaudeIDs(t *testing.T) {
	prev, had := os.LookupEnv("BEDROCK_PROVIDER")
	t.Cleanup(func() {
		if had {
			os.Setenv("BEDROCK_PROVIDER", prev)
		} else {
			os.Unsetenv("BEDROCK_PROVIDER")
		}
	})
	os.Unsetenv("BEDROCK_PROVIDER")

	cases := []struct {
		model string
		want  modelFamily
	}{
		// Dateless new-generation IDs (Fable 5 / Opus 4.8 / 4.7 have no
		// ARN-versioned variants on Bedrock).
		{"anthropic.claude-fable-5", familyAnthropic},
		{"global.anthropic.claude-fable-5", familyAnthropic},
		{"anthropic.claude-opus-4-8", familyAnthropic},
		// Bare first-party IDs a user may pick out of habit: still Claude,
		// still Anthropic schema. Converse would drop the cache markers.
		{"claude-fable-5", familyAnthropic},
		{"claude-sonnet-5", familyAnthropic},
		{"claude-opus-4-8", familyAnthropic},
		// Non-Claude models keep the Converse default.
		{"meta.llama3-70b-instruct-v1:0", familyConverse},
		{"amazon.nova-pro-v1:0", familyConverse},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, resolveFamily(tc.model), "resolveFamily(%q)", tc.model)
	}
}

// normalizeBedrockModelID upgrades bare first-party Claude IDs to the
// invokable Bedrock ID from the catalog so a `/switch --model claude-fable-5`
// doesn't die with an AWS ValidationException for a model ID that only
// exists on the first-party API.
func TestNormalizeBedrockModelID(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"claude-fable-5", "anthropic.claude-fable-5"},
		{"fable-5", "anthropic.claude-fable-5"},
		// Already-invokable IDs must pass through untouched — the user may
		// have picked an account-specific inference profile.
		{"anthropic.claude-fable-5", "anthropic.claude-fable-5"},
		{"global.anthropic.claude-opus-4-6-v1", "global.anthropic.claude-opus-4-6-v1"},
		{"us.anthropic.claude-sonnet-4-5-20250929-v1:0", "us.anthropic.claude-sonnet-4-5-20250929-v1:0"},
		// Non-Claude IDs are never rewritten.
		{"meta.llama3-70b-instruct-v1:0", "meta.llama3-70b-instruct-v1:0"},
		// Unknown Claude IDs without a catalog match stay as typed.
		{"claude-nonexistent-99", "claude-nonexistent-99"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, normalizeBedrockModelID(tc.in), "normalizeBedrockModelID(%q)", tc.in)
	}
}

// filterBedrockCapabilities strips first-party-only capability flags when a
// Claude model is mirrored onto Bedrock: fast_mode (research preview,
// first-party API only) and mid_conversation_system (not served by Bedrock
// per Anthropic's platform-availability matrix).
func TestFilterBedrockCapabilities(t *testing.T) {
	in := []string{
		"vision", "json_mode", "tools",
		"adaptive_thinking", "fast_mode",
		"mid_conversation_system", "low_cache_minimum",
	}
	got := filterBedrockCapabilities(in)
	assert.ElementsMatch(t,
		[]string{"vision", "json_mode", "tools", "adaptive_thinking", "low_cache_minimum"},
		got)
	assert.Nil(t, filterBedrockCapabilities(nil))
}

// The constructor applies the normalization so every downstream consumer
// (dispatch, catalog lookups, ListModels registration) sees the invokable
// id from the very first call.
func TestNewBedrockClientNormalizesBareClaudeID(t *testing.T) {
	c := NewBedrockClient("claude-fable-5", "us-east-1", "", zap.NewNop(), 1, 0)
	assert.Equal(t, "anthropic.claude-fable-5", c.model)

	// Profile ids the user picked from ListModels pass through untouched.
	c = NewBedrockClient("us.anthropic.claude-sonnet-4-5-20250929-v1:0", "us-east-1", "", zap.NewNop(), 1, 0)
	assert.Equal(t, "us.anthropic.claude-sonnet-4-5-20250929-v1:0", c.model)
}

// Dynamically discovered Claude ids must inherit specs from the
// first-party catalog so an unknown-but-real Claude model doesn't fall
// back to the generic 50K context default (auto-compaction on every
// turn). Non-Claude ids register without enrichment; already-known ids
// are left alone.
func TestRegisterBedrockModelEnrichment(t *testing.T) {
	// Synthetic first-party model that has no Bedrock mirror — the only
	// deterministic way to exercise the inheritance branch, since every
	// real first-party Claude id already has a Bedrock catalog entry.
	catalog.Register(catalog.ModelMeta{
		ID:              "claude-zeta-9-test",
		Aliases:         []string{"claude-zeta-9-test"},
		DisplayName:     "Claude Zeta 9 (test fixture)",
		Provider:        catalog.ProviderClaudeAI,
		ContextWindow:   777000,
		MaxOutputTokens: 77000,
		PreferredAPI:    catalog.APIAnthropicMessages,
		Capabilities:    []string{"tools", "adaptive_thinking", "fast_mode", "mid_conversation_system"},
	})

	registerBedrockModel("global.anthropic.claude-zeta-9-test", "Claude Zeta 9 (Bedrock)")
	meta, ok := catalog.Resolve(catalog.ProviderBedrock, "global.anthropic.claude-zeta-9-test")
	require.True(t, ok)
	assert.Equal(t, 777000, meta.ContextWindow, "discovered Claude id must inherit the first-party context window")
	assert.Equal(t, 77000, meta.MaxOutputTokens)
	assert.Contains(t, meta.Capabilities, "adaptive_thinking")
	assert.NotContains(t, meta.Capabilities, "fast_mode", "first-party-only capability must be filtered")
	assert.NotContains(t, meta.Capabilities, "mid_conversation_system")

	// Known ids short-circuit: re-registering must not clobber the static
	// entry's specs.
	registerBedrockModel("anthropic.claude-fable-5", "should be ignored")
	fable, ok := catalog.Resolve(catalog.ProviderBedrock, "anthropic.claude-fable-5")
	require.True(t, ok)
	assert.Equal(t, "Claude Fable 5 (Bedrock, 1M ctx)", fable.DisplayName)

	// Non-Claude ids register without first-party inheritance.
	registerBedrockModel("meta.llama-test-x9", "Meta Llama Test (Bedrock)")
	llama, ok := catalog.Resolve(catalog.ProviderBedrock, "meta.llama-test-x9")
	require.True(t, ok)
	assert.Zero(t, llama.ContextWindow)
	assert.Equal(t, catalog.APIChatCompletions, llama.PreferredAPI)
}

// The whole point of routing Fable 5 through the Anthropic InvokeModel path:
// structured system parts keep their cache_control markers on the wire.
// Bedrock supports per-block prompt caching for Claude; only the top-level
// automatic cache parameter is a first-party-only feature (never emitted
// by this client).
func TestFable5CacheMarkersSurviveRequestAssembly(t *testing.T) {
	c := &BedrockClient{model: "anthropic.claude-fable-5"}

	history := []models.Message{
		{
			Role: "system",
			SystemParts: []models.ContentBlock{
				{Type: "text", Text: "stable system prompt"},
				{Type: "text", Text: "attached context", CacheControl: &models.CacheControl{Type: "ephemeral"}},
			},
		},
		{Role: "user", Content: "hello"},
	}

	messages, systemObj := c.buildMessagesAndSystem("hello", history)
	reqBody := map[string]interface{}{
		"anthropic_version": anthropicBedrockVersion,
		"max_tokens":        4096,
		"messages":          messages,
		"system":            systemObj,
	}
	enforceCacheControlBudget(reqBody, anthropicMaxCacheBreakpoints)

	blocks, ok := reqBody["system"].([]map[string]interface{})
	assert.True(t, ok, "system must stay a structured block list")
	markers := 0
	for _, b := range blocks {
		if _, has := b["cache_control"]; has {
			markers++
		}
	}
	assert.Equal(t, 1, markers, "cache_control marker must survive request assembly")
}
