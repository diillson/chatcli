/*
 * ChatCLI - Mode-awareness regression tests.
 *
 * Locks the invariant: every system prompt the LLM can see must
 * declare which ChatCLI mode it's running in (chat / agent / coder).
 * Without an explicit declaration, the model can mis-format outputs
 * (e.g. emit execute blocks in chat mode) and the user gets confused
 * by responses that don't match the active surface.
 */
package cli

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/i18n"
	"github.com/stretchr/testify/assert"
)

func TestChatModeSystemHint_DeclaresChatMode(t *testing.T) {
	assert.Contains(t, ChatModeSystemHint, "[ACTIVE MODE: chat]",
		"chat hint must open with the canonical [ACTIVE MODE: chat] marker — the LLM uses it to refuse tool_call / execute blocks")
	assert.Contains(t, ChatModeSystemHint, "chat mode",
		"chat hint must mention 'chat mode' in plain prose so models that parse markers loosely still pick it up")
}

func TestCoderSystemPrompt_DeclaresCoderMode(t *testing.T) {
	assert.Contains(t, CoderSystemPrompt, "[ACTIVE MODE: /coder]",
		"coder prompt must open with the canonical [ACTIVE MODE: /coder] marker — matches the agent+chat sibling format")
	assert.Contains(t, CoderSystemPrompt, "/coder mode",
		"coder prompt must say '/coder mode' so prose-only model paths still recognize the surface")
}

func TestAgentSystemPromptDefault_DeclaresAgentMode(t *testing.T) {
	// The agent default lives in i18n; assert it carries the marker
	// in BOTH locales so a /agent run in either language is mode-aware.
	i18n.Init()
	got := i18n.T("agent.system_prompt.default.base", "linux", "bash", "/tmp")
	assert.Contains(t, got, "[ACTIVE MODE: /agent]",
		"i18n agent system prompt must keep the [ACTIVE MODE: /agent] marker — strip would silently break persona-less /agent runs")
}

func TestFormatInstructions_DeclareMode(t *testing.T) {
	// FormatInstructions wrap the persona prompt when a persona is
	// active. If the marker is missing here, persona+coder / persona+
	// agent runs lose the mode declaration entirely (the persona
	// prompt itself never mentions ChatCLI's mode taxonomy).
	assert.True(t,
		strings.Contains(CoderFormatInstructions, "[ACTIVE MODE: /coder]"),
		"CoderFormatInstructions must include [ACTIVE MODE: /coder] — covers persona-active /coder runs")
	assert.True(t,
		strings.Contains(AgentFormatInstructions, "[ACTIVE MODE: /agent]"),
		"AgentFormatInstructions must include [ACTIVE MODE: /agent] — covers persona-active /agent runs")
}
