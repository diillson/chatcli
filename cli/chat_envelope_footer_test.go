/*
 * ChatCLI - tests for chat envelope footer telemetry
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/ui/theme"
	"github.com/stretchr/testify/assert"
)

func TestChatEnvelopeFooter_EmptyWithoutUsage(t *testing.T) {
	cli := &ChatCLI{}
	assert.Equal(t, "", cli.chatEnvelopeFooter(nil), "no usage → no footer")
	assert.Equal(t, "", cli.chatEnvelopeFooter(&models.UsageInfo{}), "zero usage → no footer")
}

func TestChatEnvelopeFooter_ShowsCostAndContext(t *testing.T) {
	t.Cleanup(func() { theme.SetProfile(theme.DetectProfile()) })
	theme.SetProfile(theme.ProfileANSI) // keep ANSI so colorize doesn't strip in test

	cli := &ChatCLI{Provider: "OPENAI", Model: "gpt-4o"}
	footer := cli.chatEnvelopeFooter(&models.UsageInfo{PromptTokens: 1000, CompletionTokens: 500})

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
