/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// The prompts ask for a work plan, never for the model's reasoning: a
// request that demands reasoning in the reply is what Fable 5.1 / Opus 5.5
// decline as reasoning_extraction (seen in the field, Sep 24 2026). The
// old spelling stays readable so older prompts, skills and personas keep
// working.
func TestPromptsAskForAPlanNotReasoning(t *testing.T) {
	for name, prompt := range map[string]string{
		"CoderSystemPrompt":       CoderSystemPrompt,
		"CoderFormatInstructions": CoderFormatInstructions,
		"AgentFormatInstructions": AgentFormatInstructions,
	} {
		assert.Contains(t, prompt, "<plan>", "%s asks for the plan block", name)
		assert.NotContains(t, prompt, "<reasoning>", "%s must not demand a reasoning block", name)
		assert.NotContains(t, strings.ToLower(prompt), "step by step", "%s must not ask for step-by-step reasoning", name)
	}
	// The agent base prompt lives in i18n: the plan-tag fragment overrides
	// the catalog entry in every locale, and the active locale serves it.
	i18n.Init()
	base := i18n.T("agent.system_prompt.default.base")
	assert.Contains(t, base, "<plan>", "the agent base prompt asks for the plan block")
	assert.NotContains(t, base, "<reasoning>", "the agent base prompt must not demand a reasoning block")
	assert.NotEqual(t, "agent.ui.reasoning_title", i18n.T("agent.ui.reasoning_title"), "the title resolves")
	for _, loc := range []string{"en", "en-US", "pt-BR"} {
		raw, err := os.ReadFile(filepath.Join("..", "i18n", "locales", loc+".plan-tag.json"))
		require.NoError(t, err, loc)
		var frag map[string]string
		require.NoError(t, json.Unmarshal(raw, &frag), loc)
		assert.Contains(t, frag["agent.system_prompt.default.base"], "<plan>", loc)
		assert.NotContains(t, frag["agent.system_prompt.default.base"], "<reasoning>", loc)
		assert.NotContains(t, strings.ToLower(frag["agent.system_prompt.default.base"]), "step-by-step", loc)
		assert.NotEmpty(t, frag["agent.ui.reasoning_title"], loc)
	}
}

func TestPlanTagAcceptsBothSpellings(t *testing.T) {
	assert.True(t, hasPlanTag("<plan>1. read</plan>\n<tool_call name=\"@coder\" args='{}' />"))
	assert.True(t, hasPlanTag("<REASONING>1. read</REASONING>"), "case-insensitive, old spelling")
	assert.False(t, hasPlanTag("<plan>unterminated"))
	assert.False(t, hasPlanTag("no block at all"))

	assert.Equal(t, "1. read\n2. patch", strings.TrimSpace(extractPlanBlock("<plan>\n1. read\n2. patch\n</plan> rest")))
	assert.Equal(t, "old", strings.TrimSpace(extractPlanBlock("<reasoning>old</reasoning>")))
	assert.Equal(t, "new", strings.TrimSpace(extractPlanBlock("<reasoning>old</reasoning><plan>new</plan>")), "the plan spelling wins when both are present")
	assert.Empty(t, extractPlanBlock("<plan>  </plan>"), "an empty block is no plan")
	assert.Empty(t, extractPlanBlock("nothing"))

	assert.Equal(t, "a  b", stripPlanBlocks("a <plan>x</plan> <reasoning>y\nz</reasoning>b"))
}

func TestHistoryTrimmerStripsBothPlanSpellings(t *testing.T) {
	tr := NewMessageTrimmer(zap.NewNop())
	got := tr.trimAssistantMessage("<plan>1. read</plan>\n<reasoning>old</reasoning>\n<tool_call name=\"@coder\" args='{\"cmd\":\"read\"}' />")
	assert.NotContains(t, got, "<plan>")
	assert.NotContains(t, got, "<reasoning>")
	assert.Contains(t, got, "@coder read", "the tool call survives, compacted")
}

// A session recorded before the rename carries the old spelling in the
// model's plan blocks and in the format nudges ChatCLI stored as user
// turns; restoring it (and every coder run) brings both to <plan>, and
// leaves system and tool turns alone.
func TestLegacyPlanTagsNormalizedOnRestore(t *testing.T) {
	history := []models.Message{
		{Role: "system", Content: "[ACTIVE MODE: /coder]\nStart with <reasoning>"},
		{Role: "user", Content: "fix the tests"},
		{Role: "assistant", Content: "<REASONING>\n1. read\n2. patch\n</REASONING>\n<tool_call name=\"@coder\" args='{\"cmd\":\"read\"}' />"},
		{Role: "user", Content: "FORMAT ERROR: In /coder mode, you MUST write a <reasoning> block (2-6 lines with task list) BEFORE any <tool_call>. Rewrite your response starting with <reasoning>...</reasoning> then your <tool_call> tags."},
		{Role: "tool", Content: "docs say: wrap it in <reasoning>"},
		{Role: "assistant", Content: "<plan>already new</plan>"},
	}
	assert.Equal(t, 2, normalizeLegacyPlanTags(history))
	assert.Contains(t, history[0].Content, "<reasoning>", "the system slot is rebuilt each run, not rewritten")
	assert.Equal(t, "<plan>\n1. read\n2. patch\n</plan>\n<tool_call name=\"@coder\" args='{\"cmd\":\"read\"}' />", history[2].Content)
	assert.NotContains(t, history[3].Content, "reasoning")
	assert.Contains(t, history[3].Content, "<plan>...</plan>")
	assert.Contains(t, history[4].Content, "<reasoning>", "tool output is quoted material, left alone")
	assert.Equal(t, 0, normalizeLegacyPlanTags(history), "idempotent")

	cli := &ChatCLI{logger: zap.NewNop()}
	cli.restoreSessionData(&models.SessionData{ChatHistory: []models.Message{
		{Role: "assistant", Content: "<reasoning>old</reasoning>"},
	}})
	require.Len(t, cli.history, 1)
	assert.Equal(t, "<plan>old</plan>", cli.history[0].Content)
}
