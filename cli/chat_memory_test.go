/*
 * ChatCLI - chat-mode memory exception tests.
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 */
package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/cli/workspace"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func TestChatMemoryEnabled_DefaultAndOff(t *testing.T) {
	t.Setenv(chatMemoryEnvVar, "")
	if !chatMemoryEnabled() {
		t.Error("default must be ON when unset")
	}
	t.Setenv(chatMemoryEnvVar, "off")
	if chatMemoryEnabled() {
		t.Error("off must disable")
	}
}

func TestChatMemoryActive_RequiresStore(t *testing.T) {
	t.Setenv(chatMemoryEnvVar, "true")
	if (&ChatCLI{}).chatMemoryActive() {
		t.Error("no memory store must mean inactive")
	}
	cli := &ChatCLI{memoryStore: workspace.NewMemoryStore(t.TempDir(), zap.NewNop())}
	if !cli.chatMemoryActive() {
		t.Error("live memory store must activate the exception")
	}
	t.Setenv(chatMemoryEnvVar, "false")
	if cli.chatMemoryActive() {
		t.Error("env off must win over a live store")
	}
}

func TestMemoryToolDefinition_Schema(t *testing.T) {
	def := memoryToolDefinition()
	if def.Function.Name != "memory" {
		t.Fatalf("name = %q", def.Function.Name)
	}
	b, err := json.Marshal(def.Function.Parameters)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"cmd"`, `"profile"`, `"fields"`, `"remember"`, `"recall"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("parameters missing %s: %s", want, b)
		}
	}
	// The description must carry the list-op contract so the model can rewrite
	// (not just append) profile lists.
	for _, want := range []string{"goals_replace", "goals_done", "milestone"} {
		if !strings.Contains(def.Function.Description, want) {
			t.Errorf("description missing %s", want)
		}
	}
}

func TestAppendMemoryRound(t *testing.T) {
	hist, prompt := appendMemoryRound(
		[]models.Message{{Role: "system", Content: "sys"}},
		"user request", `{"cmd":"profile"}`, "Profile updated: goals")
	if len(hist) != 3 || hist[1].Content != "user request" || !strings.Contains(hist[2].Content, `{"cmd":"profile"}`) {
		t.Fatalf("history shape wrong: %+v", hist)
	}
	if !strings.Contains(prompt, "Profile updated: goals") || !strings.Contains(prompt, "confirm to the user") {
		t.Errorf("follow-up prompt = %q", prompt)
	}
}

func TestChatMemoryXMLInstruction_HasFormat(t *testing.T) {
	s := chatMemoryXMLInstruction()
	for _, want := range []string{`name="@memory"`, `"cmd":"profile"`, "goals_replace", "goals_done", "NEVER claim"} {
		if !strings.Contains(s, want) {
			t.Errorf("instruction missing %q", want)
		}
	}
}

// memoryToolFake answers the first call with a profile-update tool call and
// the second with the final confirmation, exercising the native memory loop.
type memoryToolFake struct {
	askLLMFake
	calls int
}

func (f *memoryToolFake) SupportsNativeTools() bool { return true }
func (f *memoryToolFake) SendPromptWithTools(_ context.Context, prompt string, _ []models.Message, _ []models.ToolDefinition, _ int) (*models.LLMResponse, error) {
	f.calls++
	if f.calls == 1 {
		return &models.LLMResponse{ToolCalls: []models.ToolCall{{
			Name: "memory",
			Arguments: map[string]interface{}{
				"cmd": "profile",
				"args": map[string]interface{}{
					"fields": map[string]interface{}{
						"goals_replace":  "publicar um blog pessoal (Hugo, tema claro)",
						"certifications": "Certificado Curso Gamma",
					},
				},
			},
		}}}, nil
	}
	if !strings.Contains(prompt, "memory result:") {
		return &models.LLMResponse{Content: "follow-up prompt missing memory result"}, nil
	}
	return &models.LLMResponse{Content: "perfil persistido"}, nil
}

func TestMaybeChatAskTurn_NativeMemoryLoop_PersistsProfile(t *testing.T) {
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

	fc := &memoryToolFake{}
	out, handled, err := cli.maybeChatAskTurn(context.Background(), fc, "atualiza meu perfil", "", nil, 500, SkillClientResolution{}, func() {})
	if !handled || err != nil {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if out != "perfil persistido" {
		t.Fatalf("out = %q", out)
	}
	if fc.calls != 2 {
		t.Fatalf("expected decision + follow-up calls, got %d", fc.calls)
	}

	// The write must actually land in the store — this is the whole point:
	// chat no longer promises, it persists.
	p := cli.memoryStore.Manager().Profile.Get()
	if len(p.Goals) != 1 || !strings.Contains(p.Goals[0], "blog") {
		t.Fatalf("goals not persisted: %#v", p.Goals)
	}
	if len(p.Certifications) != 1 {
		t.Fatalf("certification not persisted: %#v", p.Certifications)
	}
}

// memoryXMLFake drives the XML transport: first reply asks for a profile
// update, second confirms.
type memoryXMLFake struct{ calls int }

func (f *memoryXMLFake) GetModelName() string { return "fake" }
func (f *memoryXMLFake) SendPrompt(_ context.Context, prompt string, _ []models.Message, _ int) (string, error) {
	f.calls++
	if f.calls == 1 {
		return `<tool_call name="@memory" args='{"cmd":"profile","args":{"fields":{"interests":"fotografia"}}}' />`, nil
	}
	if !strings.Contains(prompt, "memory result:") {
		return "follow-up prompt missing memory result", nil
	}
	return "xml perfil persistido", nil
}

func TestMaybeChatAskTurn_XMLMemoryLoop_PersistsProfile(t *testing.T) {
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

	fc := &memoryXMLFake{}
	out, handled, err := cli.maybeChatAskTurn(context.Background(), fc, "anota que gosto de fotografia", "", nil, 500, SkillClientResolution{}, func() {})
	if !handled || err != nil {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if out != "xml perfil persistido" {
		t.Fatalf("out = %q", out)
	}
	p := cli.memoryStore.Manager().Profile.Get()
	if len(p.Interests) != 1 || p.Interests[0] != "fotografia" {
		t.Fatalf("interest not persisted: %#v", p.Interests)
	}
}
