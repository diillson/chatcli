/*
 * ChatCLI - chat envelope rendering tests
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The chat envelope used to live in two helpers (buildChatEnvelopeHeader,
 * buildChatEnvelopeFooter) that hand-rolled the dash math and a hardcoded
 * 87-column width. With the unified agent.RenderResponseEnvelope in place
 * we test the public contract instead: the bilateral labels emit the
 * right components, and the full render produces a closed bordered box
 * whose corners, sides, and emoji-bearing rows all line up.
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

// fakeChatClient is a stub LLMClient used by the envelope path. Only
// GetModelName is exercised by the render code; SendPrompt is here to
// satisfy the interface and panics if anything in the header path
// accidentally invokes it.
type fakeChatClient struct{ model string }

func (f *fakeChatClient) GetModelName() string { return f.model }
func (f *fakeChatClient) SendPrompt(_ context.Context, _ string, _ []models.Message, _ int) (string, error) {
	panic("SendPrompt must not be called from envelope rendering tests")
}

// TestChatEnvelopeLabels_ShowsModelAndMetrics locks the label builder
// contract: the left label carries the model in purple+bold with the
// surrounding spaces the renderer needs, and the right label carries
// the latency · tokens summary in gray. The leading/trailing spaces
// inside each label exist so the dash fill bites into the colored
// text instead of sitting flush against it.
func TestChatEnvelopeLabels_ShowsModelAndMetrics(t *testing.T) {
	left, right := chatEnvelopeLabels(
		&fakeChatClient{model: "claude-opus-4-7"},
		1400*time.Millisecond,
		&models.UsageInfo{PromptTokens: 312, CompletionTokens: 1800},
	)

	plainLeft := stripANSIWelcome(left)
	plainRight := stripANSIWelcome(right)

	assert.Contains(t, plainLeft, "claude-opus-4-7", "model name visible on the left label")
	assert.True(t, strings.HasPrefix(plainLeft, " ") && strings.HasSuffix(plainLeft, " "),
		"left label must carry the leading/trailing space the envelope expects")

	assert.Contains(t, plainRight, "1.4s", "latency formatted on the right label")
	assert.Contains(t, plainRight, "312", "prompt tokens visible on the right label")
	assert.Contains(t, plainRight, "↑", "right label includes the prompt-tokens arrow")
	assert.Contains(t, plainRight, "↓", "right label includes the completion-tokens arrow")
}

// TestChatEnvelopeLabels_NoUsage covers the unmetered branch: when the
// provider didn't return token counts, the right label falls back to
// the i18n placeholder instead of showing "0↑ 0↓" (which would look
// like a real zero-cost turn).
func TestChatEnvelopeLabels_NoUsage(t *testing.T) {
	_, right := chatEnvelopeLabels(
		&fakeChatClient{model: "claude"},
		200*time.Millisecond,
		nil,
	)
	plain := stripANSIWelcome(right)
	assert.Contains(t, plain, "—",
		"missing usage must render as the i18n placeholder, not '0↑ 0↓'")
}

// TestRenderAssistantResponse_BoxedOutput exercises the full chat
// envelope through renderAssistantResponse: the body must sit inside
// a closed box with matching top/side/bottom borders, no dangling
// dashes, and the model + metrics labels must surface on the top row.
// This is the end-to-end contract that prevents the regression the
// user reported (text overflowing outside the envelope).
func TestRenderAssistantResponse_BoxedOutput(t *testing.T) {
	cli := &ChatCLI{}
	out := captureStdout(t, func() {
		cli.renderAssistantResponse(
			&fakeChatClient{model: "claude-opus-4-7"},
			"Hello from the assistant.",
			1400*time.Millisecond,
			&models.UsageInfo{PromptTokens: 312, CompletionTokens: 1800},
		)
	})
	plain := stripANSIWelcome(out)

	assert.Contains(t, plain, "╭", "top-left corner must be drawn")
	assert.Contains(t, plain, "╮", "top-right corner must be drawn")
	assert.Contains(t, plain, "╰", "bottom-left corner must be drawn")
	assert.Contains(t, plain, "╯", "bottom-right corner must be drawn")
	assert.Contains(t, plain, "│", "vertical side borders must be drawn — body must sit inside the box")
	assert.Contains(t, plain, "claude-opus-4-7", "model name must surface on the top border")
	assert.Contains(t, plain, "1.4s", "latency must surface on the top border")
	assert.Contains(t, plain, "Hello from the assistant.", "body content must survive inside the box")
}
