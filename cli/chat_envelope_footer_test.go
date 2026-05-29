/*
 * ChatCLI - tests for chat envelope footer telemetry
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
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
