/*
 * ChatCLI - chat_ask coverage tests.
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 */

package cli

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/diillson/chatcli/models"
)

// askLLMFake implements client.LLMClient (GetModelName + SendPrompt) and
// returns canned text for the decision and follow-up turns.
type askLLMFake struct{ resp string }

func (f *askLLMFake) GetModelName() string { return "fake" }
func (f *askLLMFake) SendPrompt(_ context.Context, _ string, _ []models.Message, _ int) (string, error) {
	return f.resp, nil
}

// askToolFake adds native tool support, returning a canned LLMResponse.
type askToolFake struct {
	askLLMFake
	tool *models.LLMResponse
}

func (f *askToolFake) SupportsNativeTools() bool { return true }
func (f *askToolFake) SendPromptWithTools(_ context.Context, _ string, _ []models.Message, _ []models.ToolDefinition, _ int) (*models.LLMResponse, error) {
	return f.tool, nil
}

func newAskTestCLI() *ChatCLI {
	c := &ChatCLI{animation: NewAnimationManager()}
	c.animation.SetSuppressed(true)
	return c
}

const askXML = `<tool_call name="@ask" args='{"questions":[{"header":"H","question":"Q?","options":[{"label":"A"},{"label":"B"}]}]}' />`

func TestMaybeChatAskTurn_Disabled(t *testing.T) {
	t.Setenv(chatAskEnvVar, "false")
	t.Setenv(chatGraphViewEnvVar, "false")
	cli := newAskTestCLI()
	_, handled, err := cli.maybeChatAskTurn(context.Background(), &askLLMFake{}, "hi", "", nil, 500, SkillClientResolution{}, func() {})
	if handled || err != nil {
		t.Fatalf("disabled must not handle the turn (handled=%v err=%v)", handled, err)
	}
}

func TestMaybeChatAskTurn_XML_WithAsk(t *testing.T) {
	t.Setenv(chatAskEnvVar, "true")
	cli := newAskTestCLI()
	fc := &askLLMFake{resp: askXML}
	out, handled, err := cli.maybeChatAskTurn(context.Background(), fc, "hi", "", nil, 500, SkillClientResolution{}, func() {})
	if !handled || err != nil {
		t.Fatalf("XML ask must be handled (handled=%v err=%v)", handled, err)
	}
	// Non-TTY in CI → overlay falls back; the follow-up returns canned text.
	_ = out
}

func TestMaybeChatAskTurn_XML_NoAsk(t *testing.T) {
	t.Setenv(chatAskEnvVar, "true")
	cli := newAskTestCLI()
	fc := &askLLMFake{resp: "just a plain answer"}
	out, handled, err := cli.maybeChatAskTurn(context.Background(), fc, "hi", "", nil, 500, SkillClientResolution{}, func() {})
	if !handled || err != nil {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if out != "just a plain answer" {
		t.Fatalf("no-ask should return the plain text, got %q", out)
	}
}

func TestMaybeChatAskTurn_Native_WithAsk(t *testing.T) {
	t.Setenv(chatAskEnvVar, "true")
	cli := newAskTestCLI()
	fc := &askToolFake{
		askLLMFake: askLLMFake{resp: "final"},
		tool: &models.LLMResponse{
			ToolCalls: []models.ToolCall{{
				Name:      "ask_user",
				Arguments: map[string]interface{}{"questions": []interface{}{map[string]interface{}{"header": "H", "question": "Q?", "options": []interface{}{map[string]interface{}{"label": "A"}}}}},
			}},
		},
	}
	_, handled, err := cli.maybeChatAskTurn(context.Background(), fc, "hi", "", nil, 500, SkillClientResolution{}, func() {})
	if !handled || err != nil {
		t.Fatalf("native ask must be handled (handled=%v err=%v)", handled, err)
	}
}

func TestMaybeChatAskTurn_Native_NoAsk(t *testing.T) {
	t.Setenv(chatAskEnvVar, "true")
	cli := newAskTestCLI()
	fc := &askToolFake{askLLMFake: askLLMFake{}, tool: &models.LLMResponse{Content: "direct answer"}}
	out, handled, err := cli.maybeChatAskTurn(context.Background(), fc, "hi", "", nil, 500, SkillClientResolution{}, func() {})
	if !handled || err != nil || out != "direct answer" {
		t.Fatalf("native no-ask should return content (out=%q handled=%v err=%v)", out, handled, err)
	}
}

func TestIsAskToolName(t *testing.T) {
	for _, n := range []string{"@ask", "ask_user", "ASK", " @Ask "} {
		if !isAskToolName(n) {
			t.Errorf("%q should be an ask tool name", n)
		}
	}
	if isAskToolName("read_file") {
		t.Error("read_file is not an ask tool name")
	}
}

func TestChatAskXMLInstruction_HasFormat(t *testing.T) {
	s := chatAskXMLInstruction()
	if !strings.Contains(s, `name="@ask"`) || !strings.Contains(s, "questions") {
		t.Errorf("instruction missing the @ask format: %s", s)
	}
}

func TestShowConfigChat_NoPanic(t *testing.T) {
	cli := newAskTestCLI()
	cli.showConfigChat()       // Client nil → tool_mode XML, prints status
	cli.printConfigChatUsage() // cheat sheet
}

func TestHandleAgentAsk_FallbackAndError(t *testing.T) {
	a := &AgentMode{cli: &ChatCLI{}}
	// Valid args, non-TTY in CI → fallback (first option), no error.
	out, err := a.handleAgentAsk(context.Background(), `{"questions":[{"header":"H","question":"Q?","options":[{"label":"A"},{"label":"B"}]}]}`)
	if err != nil || !strings.Contains(out, "A") {
		t.Fatalf("fallback expected (out=%q err=%v)", out, err)
	}
	// Bad args → error result.
	if _, err := a.handleAgentAsk(context.Background(), `{"questions":[]}`); err == nil {
		t.Fatal("expected error for empty questions")
	}
}

func TestChatAskEnabled_Default(t *testing.T) {
	if v, ok := os.LookupEnv(chatAskEnvVar); ok {
		defer os.Setenv(chatAskEnvVar, v)
	} else {
		defer os.Unsetenv(chatAskEnvVar)
	}
	os.Unsetenv(chatAskEnvVar)
	if !chatAskEnabled() {
		t.Error("default should be ON when unset")
	}
}
