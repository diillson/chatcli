/*
 * ChatCLI - tests for chat envelope footer telemetry
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/compress"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/ui/theme"
	"github.com/stretchr/testify/assert"
)

func TestChatEnvelopeFooter_EmptyWithoutUsage(t *testing.T) {
	cli := &ChatCLI{}
	assert.Equal(t, "", cli.chatEnvelopeFooter("", "", nil), "no usage → no footer")
	assert.Equal(t, "", cli.chatEnvelopeFooter("", "", &models.UsageInfo{}), "zero usage → no footer")
}

func TestChatEnvelopeFooter_ShowsCostAndContext(t *testing.T) {
	t.Cleanup(func() { theme.SetProfile(theme.DetectProfile()) })
	theme.SetProfile(theme.ProfileANSI) // keep ANSI so colorize doesn't strip in test

	cli := &ChatCLI{Provider: "OPENAI", Model: "gpt-4o"}
	footer := cli.chatEnvelopeFooter("OPENAI", "gpt-4o", &models.UsageInfo{PromptTokens: 1000, CompletionTokens: 500})

	// A known-priced model yields a cost token and a context percentage.
	assert.Contains(t, footer, "$", "footer shows a cost")
	assert.Contains(t, footer, "ctx", "footer shows context fill")
}

func TestTelemetryParts_NilWhenNoUsage(t *testing.T) {
	cli := &ChatCLI{Provider: "OPENAI", Model: "gpt-4o"}
	assert.Nil(t, cli.telemetryParts(nil, 1.0, true), "no usage → no parts")
	assert.Nil(t, cli.telemetryParts(&models.UsageInfo{}, 1.0, true), "zero usage → no parts")
}

func TestTelemetryParts_IncludeTokensPrependsSummary(t *testing.T) {
	cli := &ChatCLI{Provider: "OPENAI", Model: "gpt-4o"}
	usage := &models.UsageInfo{PromptTokens: 1000, CompletionTokens: 500}

	withTokens := cli.telemetryParts(usage, 0.5, true)
	if assert.NotEmpty(t, withTokens) {
		// The leading part is the token in/out summary ("1000↑ 500↓").
		assert.Contains(t, withTokens[0], "↑", "first part is the token summary")
		assert.Contains(t, withTokens[0], "↓")
	}

	// Without tokens (chat footer path), the summary is absent.
	noTokens := cli.telemetryParts(usage, 0.5, false)
	if assert.NotEmpty(t, noTokens) {
		assert.NotContains(t, noTokens[0], "↑", "footer path omits the token summary")
	}
}

func TestTelemetryParts_ShowsCostAndContext(t *testing.T) {
	cli := &ChatCLI{Provider: "OPENAI", Model: "gpt-4o"}
	parts := cli.telemetryParts(&models.UsageInfo{PromptTokens: 1000, CompletionTokens: 500}, 0.5, true)

	joined := strings.Join(parts, " · ")
	assert.Contains(t, joined, "$", "shows the cost figure passed in")
	assert.Contains(t, joined, "ctx", "shows context fill for a known-window model")
}

func TestTelemetryParts_OmitsCostWhenZero(t *testing.T) {
	cli := &ChatCLI{Provider: "OPENAI", Model: "gpt-4o"}
	parts := cli.telemetryParts(&models.UsageInfo{PromptTokens: 1000, CompletionTokens: 500}, 0, true)
	assert.NotContains(t, strings.Join(parts, " · "), "$", "zero cost → no cost part")
}

// TestTelemetryParts_CompressionSavingsAreTurnDeltas verifies the savings
// figure is per-render (matching the per-turn cost/ctx% beside it): a second
// render with no new compression shows no savings part, and fresh savings
// report only the delta.
func TestTelemetryParts_CompressionSavingsAreTurnDeltas(t *testing.T) {
	layer := compress.NewLayer(compress.Config{
		Mode: compress.ModeLossyWithCCR, Threshold: 100, Store: compress.NewMemoryStore(),
	})
	cli := &ChatCLI{Provider: "OPENAI", Model: "gpt-4o", compressionLayer: layer}
	usage := &models.UsageInfo{PromptTokens: 1000, CompletionTokens: 500}

	// Generate savings, then render: the savings part appears.
	var b strings.Builder
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&b, "pkg/file.go:%d: match for the searched symbol\n", i+1)
	}
	if _, res := layer.CompressToolOutput("@search", b.String()); res.SavedBytes() == 0 {
		t.Fatal("fixture produced no savings")
	}
	first := strings.Join(cli.telemetryParts(usage, 0, false), " · ")
	assert.Contains(t, first, i18n.T("chat.envelope.compression_saved", ""), "first render shows fresh savings")

	// Second render with no new compression: no savings part repeated.
	second := strings.Join(cli.telemetryParts(usage, 0, false), " · ")
	assert.NotContains(t, second, i18n.T("chat.envelope.compression_saved", ""),
		"already-reported savings must not repeat on the next turn")
}

func TestFormatTurnCost_Precision(t *testing.T) {
	assert.Equal(t, "$0.0004", formatTurnCost(0.0004), "sub-cent keeps 4 decimals")
	assert.Equal(t, "$0.12", formatTurnCost(0.123), "cents keep 2 decimals")
}

func TestClampPct_Bounds(t *testing.T) {
	assert.Equal(t, 0, clampPct(-5))
	assert.Equal(t, 100, clampPct(150))
	assert.Equal(t, 12, clampPct(12.4))
	assert.Equal(t, 13, clampPct(12.6))
}

// TestTelemetryParts_ContextPctCountsCachedInput pins the ctx% semantics
// per usage schema. Bedrock Converse / Anthropic report inputTokens as the
// uncached delta only, so a 1M-window session holding 600K tokens with
// prompt caching active reports ~20K PromptTokens and ~580K cache reads:
// the footer must say 60%, not 2% (which is what users saw right before
// auto-compaction fired, and read as compaction at "2% usage").
func TestTelemetryParts_ContextPctCountsCachedInput(t *testing.T) {
	bedrock := &ChatCLI{Provider: "BEDROCK", Model: "global.anthropic.claude-sonnet-5"}
	usage := &models.UsageInfo{PromptTokens: 20000, CompletionTokens: 800, CacheReadInputTokens: 570000, CacheCreationInputTokens: 10000, IsReal: true}
	joined := strings.Join(bedrock.telemetryParts(usage, 0, false), " · ")
	assert.Contains(t, joined, "ctx 60%", "additive schema: prompt + cache read + cache write over the 1M window")

	// Subset schema: OpenAI's prompt_tokens already includes cached_tokens,
	// so adding them again would double count.
	openai := &ChatCLI{Provider: "OPENAI", Model: "gpt-4o"}
	sub := &models.UsageInfo{PromptTokens: 64000, CompletionTokens: 100, CacheReadInputTokens: 60000, IsReal: true}
	joined = strings.Join(openai.telemetryParts(sub, 0, false), " · ")
	assert.Contains(t, joined, "ctx 50%", "subset schema: prompt tokens alone over the 128K window")
}

func TestContextTokens_SchemaAware(t *testing.T) {
	u := &models.UsageInfo{PromptTokens: 100, CacheReadInputTokens: 900, CacheCreationInputTokens: 50}
	assert.Equal(t, 1050, contextTokens("CLAUDEAI", "claude-sonnet-5", u))
	assert.Equal(t, 1050, contextTokens("BEDROCK", "us.anthropic.claude-opus-5", u))
	assert.Equal(t, 1050, contextTokens("BEDROCK", "amazon.nova-pro-v1:0", u), "Converse reports every vendor's inputTokens as uncached-only")
	assert.Equal(t, 100, contextTokens("BEDROCK", "openai.gpt-5.6-sol", u), "OpenAI family on Bedrock keeps subset semantics")
	assert.Equal(t, 100, contextTokens("OPENAI", "gpt-5.5", u))
	assert.Equal(t, 100, contextTokens("OPENROUTER", "anthropic/claude-sonnet-5", u))
	assert.Equal(t, 0, contextTokens("OPENAI", "gpt-5.5", nil))
}
