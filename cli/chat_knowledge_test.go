/*
 * ChatCLI - chat-mode knowledge exception tests.
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 */
package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/models"
)

func TestChatKnowledgeEnabled_DefaultAndOff(t *testing.T) {
	t.Setenv(chatKnowledgeEnvVar, "")
	if !chatKnowledgeEnabled() {
		t.Error("default must be ON when unset")
	}
	t.Setenv(chatKnowledgeEnvVar, "off")
	if chatKnowledgeEnabled() {
		t.Error("off must disable")
	}
}

func TestChatKnowledgeActive_RequiresHandlerAndAttachment(t *testing.T) {
	t.Setenv(chatKnowledgeEnvVar, "true")
	if (&ChatCLI{}).chatKnowledgeActive() {
		t.Error("no context handler must mean inactive")
	}
	cli := newKnowledgeTestCLI(t)
	if !cli.chatKnowledgeActive() {
		t.Error("attached knowledge base must activate the exception")
	}
	cli.currentSessionName = "other-session"
	if cli.chatKnowledgeActive() {
		t.Error("session without attachments must be inactive")
	}
}

func TestIsKnowledgeToolName(t *testing.T) {
	for _, n := range []string{"@knowledge", "knowledge", " KNOWLEDGE "} {
		if !isKnowledgeToolName(n) {
			t.Errorf("%q should match", n)
		}
	}
	if isKnowledgeToolName("knowledge_base") || isKnowledgeToolName("@memory") {
		t.Error("non-knowledge names must not match")
	}
}

func TestKnowledgeToolDefinition_Schema(t *testing.T) {
	def := knowledgeToolDefinition()
	if def.Function.Name != "knowledge" {
		t.Fatalf("name = %q", def.Function.Name)
	}
	b, err := json.Marshal(def.Function.Parameters)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"cmd"`, `"search"`, `"source"`, `"offset"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("parameters missing %s: %s", want, b)
		}
	}
}

func TestAppendKnowledgeRound(t *testing.T) {
	hist, prompt := appendKnowledgeRound(
		[]models.Message{{Role: "system", Content: "sys"}},
		"user question", `{"cmd":"search"}`, "the passages")
	if len(hist) != 3 || hist[1].Content != "user question" || !strings.Contains(hist[2].Content, `{"cmd":"search"}`) {
		t.Fatalf("history shape wrong: %+v", hist)
	}
	if !strings.Contains(prompt, "the passages") || !strings.Contains(prompt, "cite source paths") {
		t.Errorf("follow-up prompt = %q", prompt)
	}
}

func TestChatKnowledgeXMLInstruction_HasFormat(t *testing.T) {
	s := chatKnowledgeXMLInstruction()
	if !strings.Contains(s, `name="@knowledge"`) || !strings.Contains(s, `"cmd":"search"`) {
		t.Errorf("instruction missing the @knowledge format: %s", s)
	}
}

// knowledgeToolFake answers the first call with a knowledge tool call and the
// second with the final content, exercising the native pull loop.
type knowledgeToolFake struct {
	askLLMFake
	calls int
}

func (f *knowledgeToolFake) SupportsNativeTools() bool { return true }
func (f *knowledgeToolFake) SendPromptWithTools(_ context.Context, prompt string, _ []models.Message, _ []models.ToolDefinition, _ int) (*models.LLMResponse, error) {
	f.calls++
	if f.calls == 1 {
		return &models.LLMResponse{ToolCalls: []models.ToolCall{{
			Name:      "knowledge",
			Arguments: map[string]interface{}{"cmd": "search", "args": map[string]interface{}{"query": "homebrew install"}},
		}}}, nil
	}
	if !strings.Contains(prompt, "knowledge result:") {
		return &models.LLMResponse{Content: "follow-up prompt missing knowledge result"}, nil
	}
	return &models.LLMResponse{Content: "final grounded answer"}, nil
}

func TestMaybeChatAskTurn_NativeKnowledgeLoop(t *testing.T) {
	t.Setenv(chatAskEnvVar, "true")
	t.Setenv(chatKnowledgeEnvVar, "true")
	cli := newKnowledgeTestCLI(t)
	cli.animation = NewAnimationManager()
	cli.animation.SetSuppressed(true)
	plugins.SetKnowledgeAdapter(&knowledgePluginAdapter{cli: cli})
	t.Cleanup(func() { plugins.SetKnowledgeAdapter(nil) })

	fc := &knowledgeToolFake{}
	out, handled, err := cli.maybeChatAskTurn(context.Background(), fc, "como instalo?", "", nil, 500, SkillClientResolution{}, func() {})
	if !handled || err != nil {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if out != "final grounded answer" {
		t.Fatalf("out = %q", out)
	}
	if fc.calls != 2 {
		t.Fatalf("expected decision + follow-up calls, got %d", fc.calls)
	}
}

// knowledgeXMLFake drives the XML transport: first reply asks for a pull,
// second returns prose.
type knowledgeXMLFake struct{ calls int }

func (f *knowledgeXMLFake) GetModelName() string { return "fake" }
func (f *knowledgeXMLFake) SendPrompt(_ context.Context, prompt string, _ []models.Message, _ int) (string, error) {
	f.calls++
	if f.calls == 1 {
		return `<tool_call name="@knowledge" args='{"cmd":"search","args":{"query":"homebrew install"}}' />`, nil
	}
	if !strings.Contains(prompt, "knowledge result:") {
		return "follow-up prompt missing knowledge result", nil
	}
	return "xml grounded answer", nil
}

func TestMaybeChatAskTurn_XMLKnowledgeLoop(t *testing.T) {
	t.Setenv(chatAskEnvVar, "false") // knowledge alone must still handle the turn
	t.Setenv(chatKnowledgeEnvVar, "true")
	cli := newKnowledgeTestCLI(t)
	cli.animation = NewAnimationManager()
	cli.animation.SetSuppressed(true)
	plugins.SetKnowledgeAdapter(&knowledgePluginAdapter{cli: cli})
	t.Cleanup(func() { plugins.SetKnowledgeAdapter(nil) })

	fc := &knowledgeXMLFake{}
	out, handled, err := cli.maybeChatAskTurn(context.Background(), fc, "como instalo?", "", nil, 500, SkillClientResolution{}, func() {})
	if !handled || err != nil {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if out != "xml grounded answer" {
		t.Fatalf("out = %q", out)
	}
}

func TestMaybeChatAskTurn_BothDisabled(t *testing.T) {
	t.Setenv(chatAskEnvVar, "false")
	t.Setenv(chatKnowledgeEnvVar, "false")
	cli := newKnowledgeTestCLI(t)
	cli.animation = NewAnimationManager()
	if _, handled, _ := cli.maybeChatAskTurn(context.Background(), &askLLMFake{}, "hi", "", nil, 500, SkillClientResolution{}, func() {}); handled {
		t.Fatal("with both exceptions off the normal path must run")
	}
}
