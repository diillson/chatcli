/*
 * ChatCLI - empty-reply regression tests for the chat/MoA exception loops.
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 */
package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/agent/moa"
	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/cli/workspace"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

const closingAnswer = "answer from the gathered material"

// greedyNativeFake keeps answering every tool round with a call and no text —
// the shape that used to reach the user as an empty reply once the tool's
// round budget ran out. The plain transport (the closing round) answers.
type greedyNativeFake struct {
	toolName     string
	withTools    int
	plain        int
	plainPrompts []string
}

func (f *greedyNativeFake) GetModelName() string      { return "fake" }
func (f *greedyNativeFake) SupportsNativeTools() bool { return true }
func (f *greedyNativeFake) SendPrompt(_ context.Context, prompt string, _ []models.Message, _ int) (string, error) {
	f.plain++
	f.plainPrompts = append(f.plainPrompts, prompt)
	return closingAnswer, nil
}
func (f *greedyNativeFake) SendPromptWithTools(_ context.Context, _ string, _ []models.Message, _ []models.ToolDefinition, _ int) (*models.LLMResponse, error) {
	f.withTools++
	return &models.LLMResponse{ToolCalls: []models.ToolCall{{
		Name:      f.toolName,
		Arguments: map[string]interface{}{"cmd": "recall", "args": map[string]interface{}{"query": "q"}},
	}}}, nil
}

// greedyXMLFake is the XML-transport twin: it answers with ONLY a tool tag
// until the closing directive arrives.
type greedyXMLFake struct {
	tag   string
	calls int
}

func (f *greedyXMLFake) GetModelName() string { return "fake" }
func (f *greedyXMLFake) SendPrompt(_ context.Context, prompt string, _ []models.Message, _ int) (string, error) {
	f.calls++
	if strings.Contains(prompt, "No more tool calls are available") {
		if strings.Contains(prompt, "<tool_call name=") {
			return "closing prompt re-advertised the tools", nil
		}
		return closingAnswer, nil
	}
	return f.tag, nil
}

func newMemoryChatCLI(t *testing.T) *ChatCLI {
	t.Helper()
	t.Setenv(chatAskEnvVar, "false")
	t.Setenv(chatKnowledgeEnvVar, "false")
	t.Setenv(chatGraphViewEnvVar, "false")
	t.Setenv(chatMemoryEnvVar, "true")
	cli := &ChatCLI{
		logger:      zap.NewNop(),
		memoryStore: workspace.NewMemoryStore(t.TempDir(), zap.NewNop()),
	}
	cli.animation = NewAnimationManager()
	cli.animation.SetSuppressed(true)
	plugins.SetMemoryAdapter(&memoryPluginAdapter{cli: cli})
	t.Cleanup(func() { plugins.SetMemoryAdapter(nil) })
	return cli
}

func TestChatNative_ToolPastBudget_GetsClosingAnswer(t *testing.T) {
	cli := newMemoryChatCLI(t)
	fc := &greedyNativeFake{toolName: "memory"}
	out, handled, err := cli.maybeChatAskTurn(context.Background(), fc, "o que você lembra de mim?", "", nil, 500, SkillClientResolution{}, func() {})
	if !handled || err != nil {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if out != closingAnswer {
		t.Fatalf("a call past the budget must end in a closing answer, got %q", out)
	}
	if fc.withTools != chatMemoryMaxRounds+1 {
		t.Fatalf("expected %d tool rounds (budget + the refused one), got %d", chatMemoryMaxRounds+1, fc.withTools)
	}
	if fc.plain != 1 || !strings.Contains(fc.plainPrompts[0], "memory result:") {
		t.Fatalf("closing round must run once and carry the last tool result: %d %q", fc.plain, fc.plainPrompts)
	}
}

func TestChatNative_UnofferedToolCall_GetsClosingAnswer(t *testing.T) {
	cli := newMemoryChatCLI(t)
	fc := &greedyNativeFake{toolName: "web_search"}
	out, _, err := cli.maybeChatAskTurn(context.Background(), fc, "q", "", nil, 500, SkillClientResolution{}, func() {})
	if err != nil || out != closingAnswer {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if fc.withTools != 1 {
		t.Fatalf("an unoffered call must not loop, got %d rounds", fc.withTools)
	}
}

func TestChatXML_TagOnlyReplies_GetClosingAnswer(t *testing.T) {
	for name, tag := range map[string]string{
		"past budget": `<tool_call name="@memory" args='{"cmd":"recall","args":{"query":"q"}}' />`,
		"unoffered":   `<tool_call name="@coder" args='{"cmd":"read","args":{"file":"x"}}' />`,
	} {
		t.Run(name, func(t *testing.T) {
			cli := newMemoryChatCLI(t)
			fc := &greedyXMLFake{tag: tag}
			out, _, err := cli.maybeChatAskTurn(context.Background(), fc, "q", "", nil, 500, SkillClientResolution{}, func() {})
			if err != nil || out != closingAnswer {
				t.Fatalf("out=%q err=%v (calls=%d)", out, err, fc.calls)
			}
		})
	}
}

func TestChatToolBudget_IsPerTool(t *testing.T) {
	var b chatToolBudget
	for i := 0; i < chatKnowledgeMaxRounds; i++ {
		if !b.takeKnowledge() {
			t.Fatalf("knowledge round %d refused", i)
		}
	}
	if b.takeKnowledge() {
		t.Fatal("knowledge must stop at its cap")
	}
	if !b.takeContextPull() || !b.takeMemory() {
		t.Fatal("another tool's rounds must not consume this tool's budget")
	}
	if !b.takeGraphView() || b.takeGraphView() {
		t.Fatal("graphview renders exactly once")
	}
}

func TestMoaNative_EmptyAfterTools_GetsClosingAnswer(t *testing.T) {
	cli := &ChatCLI{logger: zap.NewNop()}
	fc := &greedyNativeFake{toolName: "memory"}
	out, err := cli.runMoaTurnNative(context.Background(), fc, moa.Ref{}, "q", nil, moaToolset{memory: true})
	if err != nil || out != closingAnswer {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if fc.withTools != moaToolMaxRounds+1 {
		t.Fatalf("rounds=%d", fc.withTools)
	}
}

func TestMoaXML_TagOnly_GetsClosingAnswer(t *testing.T) {
	cli := &ChatCLI{logger: zap.NewNop()}
	fc := &greedyXMLFake{tag: `<tool_call name="@memory" args='{"query":"q"}' />`}
	out, err := cli.runMoaTurnXML(context.Background(), fc, moa.Ref{}, "q", nil, moaToolset{memory: true})
	if err != nil || out != closingAnswer {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestHandleChatTurnResult_BlankReplyNotStored(t *testing.T) {
	cli := &ChatCLI{logger: zap.NewNop()}
	cli.handleChatTurnResult(context.Background(), nil, models.Message{Role: "user", Content: "q"},
		"  \n", &askLLMFake{}, SkillClientResolution{}, "q", "", 0)
	if len(cli.history) != 0 {
		t.Fatalf("a blank reply must not be stored: %#v", cli.history)
	}
}
