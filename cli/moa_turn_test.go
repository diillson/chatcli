/*
 * ChatCLI - MoA tool-aware turn executor tests.
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 */
package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/agent/moa"
	"github.com/diillson/chatcli/cli/compress"
	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/models"
)

// moaCompFake implements plugins.CompressionAdapter with one stored entry.
type moaCompFake struct {
	key     string
	content string
}

func (f *moaCompFake) Recall(key string) (string, bool) {
	if key == f.key {
		return f.content, true
	}
	return "", false
}
func (f *moaCompFake) Compress(_, content string) (string, error) { return content, nil }
func (f *moaCompFake) Stats() string                              { return "" }

func TestHistoryHasCCRMarkers(t *testing.T) {
	marker := compress.FormatMarker(compress.KeyFor("offloaded content"))
	with := []models.Message{{Role: "assistant", Content: "see " + marker + " for detail"}}
	if !historyHasCCRMarkers(with) {
		t.Error("a valid marker in history must be detected")
	}
	without := []models.Message{{Role: "assistant", Content: "plain text, even <<ccr:short>> malformed"}}
	if historyHasCCRMarkers(without) {
		t.Error("malformed markers must not count")
	}
}

func TestMoaToolsetForRun_RecallGates(t *testing.T) {
	marker := compress.FormatMarker(compress.KeyFor("x"))
	hist := []models.Message{{Role: "assistant", Content: marker}}

	// No compression layer wired → recall stays off even with markers present.
	if ts := (&ChatCLI{}).moaToolsetForRun(hist); ts.recall {
		t.Error("recall must require a wired compression layer")
	}

	cli := &ChatCLI{compressionLayer: compress.NewLayerFromEnv(t.TempDir())}
	if ts := cli.moaToolsetForRun(hist); !ts.recall {
		t.Error("layer + marker in history must grant recall")
	}
	if ts := cli.moaToolsetForRun(nil); ts.recall {
		t.Error("no markers in history must mean no recall tool")
	}
}

func TestMoaToolsetLabel(t *testing.T) {
	if got := moaToolsetLabel(moaToolset{}); got != "" {
		t.Errorf("empty toolset must render empty, got %q", got)
	}
	if got := moaToolsetLabel(moaToolset{knowledge: true, recall: true, memory: true}); got != "knowledge, recall, memory" {
		t.Errorf("label = %q", got)
	}
}

func TestMoaToolsetForRun_MemoryGate(t *testing.T) {
	t.Setenv("CHATCLI_MEMORY_MODE", "off")
	if ts := (&ChatCLI{}).moaToolsetForRun(nil); ts.memory {
		t.Error("memory mode off must not grant the memory tool")
	}
	t.Setenv("CHATCLI_MEMORY_MODE", "index")
	if ts := (&ChatCLI{}).moaToolsetForRun(nil); !ts.memory {
		t.Error("memory mode index must grant the memory tool")
	}
}

// moaMemFake implements plugins.MemoryAdapter and records which methods run —
// panel turns must only ever reach Recall.
type moaMemFake struct {
	recalled string
	mutated  bool
}

func (f *moaMemFake) Remember(string, string) (string, error) {
	f.mutated = true
	return "", nil
}
func (f *moaMemFake) UpdateProfile(map[string]string) (string, error) {
	f.mutated = true
	return "", nil
}
func (f *moaMemFake) Forget(string) (string, error) {
	f.mutated = true
	return "", nil
}
func (f *moaMemFake) Recall(query string) (string, error) {
	f.recalled = query
	return "User is a platform SRE; prefers keyless backends", nil
}

// runMoaMemory must pin the subcommand to recall — even if a model smuggles a
// mutating cmd into the args, only Recall may execute.
func TestRunMoaMemory_ReadOnlyByConstruction(t *testing.T) {
	fake := &moaMemFake{}
	plugins.SetMemoryAdapter(fake)
	t.Cleanup(func() { plugins.SetMemoryAdapter(nil) })

	out := runMoaMemory(context.Background(), `{"cmd":"forget","query":"certifications"}`)
	if fake.mutated {
		t.Fatal("a mutating memory subcommand must never execute from a panel turn")
	}
	if fake.recalled != "certifications" || !strings.Contains(out, "platform SRE") {
		t.Errorf("recall not executed as expected: recalled=%q out=%q", fake.recalled, out)
	}
}

func TestAppendMoaToolRound_DedupsTrailingUserTurn(t *testing.T) {
	// The shared enriched history already ends with the user's request: the
	// fold must not duplicate it.
	hist := []models.Message{{Role: "user", Content: "the question"}}
	next, followup := appendMoaToolRound(hist, "the question", "recall", `{"key":"k"}`, "original")
	if len(next) != 2 || next[1].Role != "assistant" || !strings.Contains(next[1].Content, "[recall call]") {
		t.Fatalf("history shape wrong: %+v", next)
	}
	if !strings.Contains(followup, "recall result:\noriginal") {
		t.Errorf("follow-up = %q", followup)
	}

	// A pending prompt that is NOT the trailing turn (later tool rounds) is folded in.
	next, _ = appendMoaToolRound(next, followup, "knowledge", `{"cmd":"search"}`, "passages")
	if len(next) != 4 || next[2].Role != "user" || next[2].Content != followup {
		t.Fatalf("pending prompt must become a user turn: %+v", next)
	}
}

func TestRecallToolDefinition_Schema(t *testing.T) {
	def := recallToolDefinition()
	if def.Function.Name != "recall" {
		t.Fatalf("name = %q", def.Function.Name)
	}
	b, err := json.Marshal(def.Function.Parameters)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"key"`) {
		t.Errorf("parameters missing key: %s", b)
	}
}

func TestMoaRecallXMLInstruction_HasFormat(t *testing.T) {
	s := moaRecallXMLInstruction()
	if !strings.Contains(s, `name="@recall"`) || !strings.Contains(s, `"key"`) {
		t.Errorf("instruction missing the @recall format: %s", s)
	}
}

// moaToolCaptureFake records the tool definitions offered on a native turn.
type moaToolCaptureFake struct {
	askLLMFake
	seenTools []models.ToolDefinition
	sendCalls int
}

func (f *moaToolCaptureFake) SupportsNativeTools() bool { return true }
func (f *moaToolCaptureFake) SendPromptWithTools(_ context.Context, _ string, _ []models.Message, tools []models.ToolDefinition, _ int) (*models.LLMResponse, error) {
	f.seenTools = tools
	return &models.LLMResponse{Content: "native answer"}, nil
}
func (f *moaToolCaptureFake) SendPrompt(_ context.Context, _ string, _ []models.Message, _ int) (string, error) {
	f.sendCalls++
	return "plain answer", nil
}

func TestMoaTurn_OffersGrantedToolsOnly(t *testing.T) {
	fc := &moaToolCaptureFake{}
	cli := &ChatCLI{Provider: "fake", Model: "m", Client: fc}
	turn := cli.moaTurn(moaToolset{knowledge: true, recall: true})
	out, err := turn(context.Background(), moa.Ref{Provider: "fake", Model: "m"}, "q", nil)
	if err != nil || out != "native answer" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	names := make([]string, 0, len(fc.seenTools))
	for _, d := range fc.seenTools {
		names = append(names, d.Function.Name)
	}
	if len(names) != 2 || names[0] != "knowledge" || names[1] != "recall" {
		t.Errorf("offered tools = %v", names)
	}
}

func TestMoaTurn_NoToolsUsesPlainSendPrompt(t *testing.T) {
	fc := &moaToolCaptureFake{}
	cli := &ChatCLI{Provider: "fake", Model: "m", Client: fc}
	turn := cli.moaTurn(moaToolset{})
	out, err := turn(context.Background(), moa.Ref{Provider: "fake", Model: "m"}, "q", nil)
	if err != nil || out != "plain answer" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if fc.sendCalls != 1 || fc.seenTools != nil {
		t.Errorf("an empty toolset must use the single-shot path (sendCalls=%d, tools=%v)", fc.sendCalls, fc.seenTools)
	}
}

// moaMemoryToolFake drives the native loop: round one asks for a memory
// recall, round two must see the recalled notes folded in and answers.
type moaMemoryToolFake struct {
	askLLMFake
	calls int
}

func (f *moaMemoryToolFake) SupportsNativeTools() bool { return true }
func (f *moaMemoryToolFake) SendPromptWithTools(_ context.Context, prompt string, _ []models.Message, _ []models.ToolDefinition, _ int) (*models.LLMResponse, error) {
	f.calls++
	if f.calls == 1 {
		return &models.LLMResponse{ToolCalls: []models.ToolCall{{
			Name:      "memory",
			Arguments: map[string]interface{}{"query": "user preferences"},
		}}}, nil
	}
	if !strings.Contains(prompt, "memory result:") || !strings.Contains(prompt, "platform SRE") {
		return &models.LLMResponse{Content: "follow-up missing memory result"}, nil
	}
	return &models.LLMResponse{Content: "answer grounded in user notes"}, nil
}

func TestMoaTurn_NativeMemoryRecallLoop(t *testing.T) {
	fake := &moaMemFake{}
	plugins.SetMemoryAdapter(fake)
	t.Cleanup(func() { plugins.SetMemoryAdapter(nil) })

	fc := &moaMemoryToolFake{}
	cli := &ChatCLI{Provider: "fake", Model: "m", Client: fc}
	turn := cli.moaTurn(moaToolset{memory: true})
	out, err := turn(context.Background(), moa.Ref{Provider: "fake", Model: "m"}, "what stack does the user prefer?", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "answer grounded in user notes" {
		t.Fatalf("out = %q", out)
	}
	if fake.recalled != "user preferences" || fake.mutated {
		t.Fatalf("recall must run read-only: recalled=%q mutated=%v", fake.recalled, fake.mutated)
	}
}

// moaRecallToolFake drives the native loop: first call requests a recall,
// second must see the recalled original and answers.
type moaRecallToolFake struct {
	askLLMFake
	calls int
	key   string
}

func (f *moaRecallToolFake) SupportsNativeTools() bool { return true }
func (f *moaRecallToolFake) SendPromptWithTools(_ context.Context, prompt string, _ []models.Message, _ []models.ToolDefinition, _ int) (*models.LLMResponse, error) {
	f.calls++
	if f.calls == 1 {
		return &models.LLMResponse{ToolCalls: []models.ToolCall{{
			Name:      "recall",
			Arguments: map[string]interface{}{"key": f.key},
		}}}, nil
	}
	if !strings.Contains(prompt, "recall result:") || !strings.Contains(prompt, "the offloaded original") {
		return &models.LLMResponse{Content: "follow-up missing recall result"}, nil
	}
	return &models.LLMResponse{Content: "grounded final answer"}, nil
}

func TestMoaTurn_NativeRecallLoop(t *testing.T) {
	key := compress.KeyFor("payload")
	plugins.SetCompressionAdapter(&moaCompFake{key: key, content: "the offloaded original"})
	t.Cleanup(func() { plugins.SetCompressionAdapter(nil) })

	fc := &moaRecallToolFake{key: key}
	cli := &ChatCLI{Provider: "fake", Model: "m", Client: fc}
	turn := cli.moaTurn(moaToolset{recall: true})
	out, err := turn(context.Background(), moa.Ref{Provider: "fake", Model: "m"}, "what did that output say?", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "grounded final answer" {
		t.Fatalf("out = %q", out)
	}
	if fc.calls != 2 {
		t.Fatalf("expected decision + follow-up calls, got %d", fc.calls)
	}
}

// A MoA participant must be able to ground its answer in the attached
// knowledge bases, exactly like a chat turn: the fake asks for a search on
// round one and only answers once the passages are folded back in.
func TestMoaTurn_NativeKnowledgeLoop(t *testing.T) {
	t.Setenv(chatKnowledgeEnvVar, "true")
	cli := newKnowledgeTestCLI(t)
	plugins.SetKnowledgeAdapter(&knowledgePluginAdapter{cli: cli})
	t.Cleanup(func() { plugins.SetKnowledgeAdapter(nil) })

	fc := &knowledgeToolFake{}
	cli.Provider, cli.Model, cli.Client = "fake", "m", fc

	ts := cli.moaToolsetForRun(nil)
	if !ts.knowledge {
		t.Fatal("an attached knowledge base must grant the knowledge tool")
	}
	turn := cli.moaTurn(ts)
	out, err := turn(context.Background(), moa.Ref{Provider: "fake", Model: "m"}, "como instalo?", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "final grounded answer" {
		t.Fatalf("out = %q", out)
	}
	if fc.calls != 2 {
		t.Fatalf("expected decision + follow-up calls, got %d", fc.calls)
	}
}

// moaRecallXMLFake drives the XML transport: the instruction must be pinned,
// the call is answered with the full marker form, then prose.
type moaRecallXMLFake struct {
	calls int
	key   string
}

func (f *moaRecallXMLFake) GetModelName() string { return "fake" }
func (f *moaRecallXMLFake) SendPrompt(_ context.Context, prompt string, _ []models.Message, _ int) (string, error) {
	f.calls++
	if f.calls == 1 {
		if !strings.Contains(prompt, "CCR recall is ENABLED") {
			return "missing recall instruction", nil
		}
		return `<tool_call name="@recall" args='{"key":"<<ccr:` + f.key + `>>"}' />`, nil
	}
	if !strings.Contains(prompt, "recall result:") {
		return "follow-up missing recall result", nil
	}
	return "xml grounded answer", nil
}

func TestMoaTurn_XMLRecallLoop(t *testing.T) {
	key := compress.KeyFor("payload")
	plugins.SetCompressionAdapter(&moaCompFake{key: key, content: "the offloaded original"})
	t.Cleanup(func() { plugins.SetCompressionAdapter(nil) })

	fc := &moaRecallXMLFake{key: key}
	cli := &ChatCLI{Provider: "fake", Model: "m", Client: fc}
	turn := cli.moaTurn(moaToolset{recall: true})
	out, err := turn(context.Background(), moa.Ref{Provider: "fake", Model: "m"}, "what did that output say?", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "xml grounded answer" {
		t.Fatalf("out = %q", out)
	}
}
