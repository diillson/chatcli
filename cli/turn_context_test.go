/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/workspace"
	llmclient "github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// TestChatSystemPrompt_ByteStableAcrossTurns is the contract behind the
// prefix cache: two consecutive chat turns must assemble a system message
// with identical bytes, with everything per-turn in the turn context.
func TestChatSystemPrompt_ByteStableAcrossTurns(t *testing.T) {
	cli, _ := newPipelineCLI(t, nil)
	ch, err := NewContextHandler(zap.NewNop())
	if err != nil {
		t.Skipf("NewContextHandler unavailable: %v", err)
	}
	cli.contextHandler = ch
	if cli.contextBuilder == nil {
		bl := workspace.NewBootstrapLoader(t.TempDir(), t.TempDir(), zap.NewNop())
		cli.contextBuilder = workspace.NewContextBuilder(bl, workspace.NewMemoryStore(t.TempDir(), zap.NewNop()), t.TempDir())
	}
	first := cli.assembleChatSystemPrompt(testCtx(), "first question about the parser", "")
	sys1 := combinedSystemMessage(first.parts)
	cli.history = append(cli.history,
		models.TurnContextMessage(turnContextText(first.turnContext)),
		models.Message{Role: "user", Content: "first question about the parser"},
		models.Message{Role: "assistant", Content: "answer one"})
	second := cli.assembleChatSystemPrompt(testCtx(), "now something about deployments", "")
	sys2 := combinedSystemMessage(second.parts)
	if sys1.Content != sys2.Content {
		t.Fatalf("system message must be byte-stable across turns:\n--- turn 1 ---\n%s\n--- turn 2 ---\n%s", sys1.Content, sys2.Content)
	}
	for i, p := range second.parts {
		if p.CacheControl == nil {
			t.Fatalf("system part %d carries no cache hint: volatile content leaked into the system message: %q", i, p.Text)
		}
	}
	if strings.Contains(sys2.Content, "Current date") {
		t.Fatal("the date must not be in the system message")
	}
	ctxText := turnContextText(second.turnContext)
	if !strings.HasPrefix(ctxText, turnContextHeader) || !strings.Contains(ctxText, "Current date:") {
		t.Fatalf("turn context must carry the header and the date: %q", ctxText)
	}
	if strings.Contains(ctxText, ":") && strings.Contains(ctxText, "Current date:") {
		line := ctxText[strings.Index(ctxText, "Current date:"):]
		if len(strings.Fields(line)) > 0 && strings.Count(strings.SplitN(line, "\n", 2)[0], ":") > 1 {
			t.Fatalf("date must be day-resolution, no clock: %q", strings.SplitN(line, "\n", 2)[0])
		}
	}
	// Temp history: [system, ..., turn context, user]; the injected message is flagged.
	temp := cli.buildChatTempHistoryWithContext(second.parts, second.turnContext, "now something about deployments", "", nil)
	last, prev := temp[len(temp)-1], temp[len(temp)-2]
	if last.Role != "user" || last.Content != "now something about deployments" || !prev.IsTurnContext() {
		t.Fatalf("turn context must sit right before the user's turn: %+v %+v", prev, last)
	}
	// Hints ignore the injected block; the extraction snippet skips it.
	hints := cli.turnHints("x")
	for _, h := range hints {
		if strings.Contains(strings.ToLower(h), "turn context") {
			t.Fatalf("hints must not come from injected context: %v", hints)
		}
	}
	snippet := buildExtractionSnippet(cli.history)
	if strings.Contains(snippet.String(), turnContextHeader) {
		t.Fatal("memory extraction must skip injected turn context")
	}
	if lastUserMessage([]models.Message{{Role: "user", Content: "real"}, models.TurnContextMessage("ctx")}) != "real" {
		t.Fatal("lastUserMessage must skip injected context")
	}
}

func TestPromptCacheKey_StableWhenVolatileContentChanges(t *testing.T) {
	stable := models.ContentBlock{Type: "text", Text: "STABLE SYSTEM", CacheControl: &models.CacheControl{Type: "ephemeral"}}
	h1 := []models.Message{{Role: "system", Content: "STABLE SYSTEM\n\nvolatile A", SystemParts: []models.ContentBlock{stable, {Type: "text", Text: "volatile A"}}}}
	h2 := []models.Message{{Role: "system", Content: "STABLE SYSTEM\n\nvolatile B", SystemParts: []models.ContentBlock{stable, {Type: "text", Text: "volatile B"}}}}
	k1, k2 := llmclient.PromptCacheKey(h1), llmclient.PromptCacheKey(h2)
	if k1 == "" || k1 != k2 {
		t.Fatalf("key must follow the marked parts only: %q vs %q", k1, k2)
	}
	if llmclient.PromptCacheKey([]models.Message{{Role: "system", Content: "plain"}}) == "" {
		t.Fatal("flat content keeps working when nothing is marked")
	}
}

func TestPrefixBudget_RatioFrozenPerSession(t *testing.T) {
	cli := &ChatCLI{stateRoot: t.TempDir(), Provider: "openai", Model: "gpt-5.6-terra"}
	b1 := cli.newPrefixBudget("openai", "gpt-5.6-terra")
	cli.calibrator().Observe("openai", "gpt-5.6-terra", 20_000, 10_000) // ratio 2.0 learned mid-session
	b2 := cli.newPrefixBudget("openai", "gpt-5.6-terra")
	if b1.MaxChars != b2.MaxChars {
		t.Fatalf("the prefix budget must not move with the calibrator mid-session: %d vs %d", b1.MaxChars, b2.MaxChars)
	}
	if cli.newPrefixBudget("openai", "gpt-5.5").MaxChars == 0 {
		t.Fatal("another model keys its own ratio")
	}
}

func TestAgentSystemMessage_NoVolatileBlocks(t *testing.T) {
	msg := buildAgentSystemMessage("core", "tools", "workspace", "skills", "orch", "", "")
	for _, p := range msg.SystemParts {
		if strings.Contains(p.Text, "Current date") {
			t.Fatal("agent system message must not carry the date")
		}
	}
	ctx := models.TurnContextMessage(turnContextHeader + "Current date: 2026-09-03")
	if !ctx.IsTurnContext() || ctx.Role != "user" {
		t.Fatal("turn context message shape")
	}
}
