/*
 * ChatCLI - chat envelope header/footer rendering tests
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/models"
	"github.com/stretchr/testify/assert"
)

// fakeChatClient is a stub LLMClient used by buildChatEnvelopeHeader.
// Only GetModelName is exercised by the envelope code; SendPrompt is
// here just to satisfy the interface and panics if anything in the
// header path accidentally invokes it.
type fakeChatClient struct{ model string }

func (f *fakeChatClient) GetModelName() string { return f.model }
func (f *fakeChatClient) SendPrompt(_ context.Context, _ string, _ []models.Message, _ int) (string, error) {
	panic("SendPrompt must not be called from envelope rendering tests")
}

func TestBuildChatEnvelopeHeader_ShowsModelAndMetrics(t *testing.T) {
	cli := &ChatCLI{}
	header := cli.buildChatEnvelopeHeader(
		&fakeChatClient{model: "claude-opus-4-7"},
		1400*time.Millisecond,
		&models.UsageInfo{PromptTokens: 312, CompletionTokens: 1800},
	)

	plain := stripANSIWelcome(header)
	assert.True(t, strings.HasPrefix(plain, "╭─"),
		"header must open with the rounded corner ╭─")
	assert.True(t, strings.HasSuffix(plain, "─╮"),
		"header must close with ─╮")
	assert.Contains(t, plain, "claude-opus-4-7", "model name visible")
	assert.Contains(t, plain, "1.4s", "latency formatted")
	assert.Contains(t, plain, "312", "prompt tokens visible")
}

func TestBuildChatEnvelopeFooter_MatchesHeaderWidth(t *testing.T) {
	cli := &ChatCLI{}
	header := cli.buildChatEnvelopeHeader(
		&fakeChatClient{model: "gpt-4o"},
		800*time.Millisecond,
		&models.UsageInfo{PromptTokens: 100, CompletionTokens: 50},
	)
	footer := cli.buildChatEnvelopeFooter(header)

	plainHead := stripANSIWelcome(header)
	plainFoot := stripANSIWelcome(footer)
	assert.Equal(t, visibleLen(plainHead), visibleLen(plainFoot),
		"footer width must match the header — same envelope shape")
	assert.True(t, strings.HasPrefix(plainFoot, "╰"),
		"footer opens with ╰")
	assert.True(t, strings.HasSuffix(plainFoot, "╯"),
		"footer closes with ╯")
}

func TestBuildChatEnvelopeHeader_NoUsage(t *testing.T) {
	cli := &ChatCLI{}
	header := cli.buildChatEnvelopeHeader(
		&fakeChatClient{model: "claude"},
		200*time.Millisecond,
		nil,
	)
	plain := stripANSIWelcome(header)
	assert.Contains(t, plain, "—",
		"missing usage must render as the i18n placeholder, not '0↑ 0↓'")
}
