package cmd

import (
	"context"
	"testing"

	"strings"

	"github.com/diillson/chatcli/cli/rpcserve"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/manager"
	"github.com/diillson/chatcli/models"
)

// fakeClient is a minimal client.LLMClient.
type fakeClient struct {
	reply    string
	lastHist int
}

func (f *fakeClient) GetModelName() string { return "fake" }
func (f *fakeClient) SendPrompt(_ context.Context, _ string, history []models.Message, _ int) (string, error) {
	f.lastHist = len(history)
	return f.reply, nil
}

// fakeManager embeds the interface (so unimplemented methods exist) and only
// overrides GetClient, which is all rpcBackend.Prompt uses.
type fakeManager struct {
	manager.LLMManager
	client      *fakeClient
	noProviders bool
}

func (m *fakeManager) GetClient(string, string) (client.LLMClient, error) { return m.client, nil }

// providers defaults to one fake provider so HasLLM is true; tests pin the
// no-provider surface by setting noProviders.
func (m *fakeManager) GetAvailableProviders() []string {
	if m.noProviders {
		return nil
	}
	return []string{"FAKE"}
}

func TestFirstNonEmpty(t *testing.T) {
	if firstNonEmpty("", "", "x", "y") != "x" {
		t.Error("should return first non-empty")
	}
	if firstNonEmpty("", "") != "" {
		t.Error("all empty -> empty")
	}
}

func TestRPCBackend_NoCLI(t *testing.T) {
	b := &rpcBackend{mgr: &fakeManager{client: &fakeClient{}}, sessions: map[string][]models.Message{}} // cli is nil
	if _, err := b.Agent(context.Background(), "s", "t", rpcserve.RunOpts{}); err == nil {
		t.Error("Agent should error when ChatCLI is unavailable")
	}
	if _, err := b.Coder(context.Background(), "s", "t", rpcserve.RunOpts{}); err == nil {
		t.Error("Coder should error when ChatCLI is unavailable")
	}
	if _, err := b.AgentStream(context.Background(), "s", "t", rpcserve.RunOpts{}); err == nil {
		t.Error("AgentStream should error when ChatCLI is unavailable")
	}
	if _, err := b.CallTool(context.Background(), "read", "x"); err == nil {
		t.Error("CallTool should error when ChatCLI is unavailable")
	}
	if b.Tools() != nil {
		t.Error("Tools should be nil when ChatCLI is unavailable")
	}
	if b.Skills() != nil {
		t.Error("Skills should be nil when ChatCLI is unavailable")
	}
	if _, err := b.SkillContent("x"); err == nil {
		t.Error("SkillContent should error when ChatCLI is unavailable")
	}
	if _, err := b.ProvidersJSON(); err == nil {
		t.Error("ProvidersJSON should error when ChatCLI is unavailable")
	}
}

// TestRPCBackend_PromptWithRouting pins the per-call provider/model routing
// on the chat path: options select a distinct client via the manager while
// the shared session history is preserved.
// TestRPCBackend_NoProvider pins the actionable error every LLM-backed
// call returns when the instance has no provider configured — direct tools
// are unaffected and HasLLM drives the MCP tool listing.
func TestRPCBackend_NoProvider(t *testing.T) {
	b := &rpcBackend{mgr: &fakeManager{noProviders: true}, sessions: map[string][]models.Message{}}
	if b.HasLLM() {
		t.Fatal("HasLLM must be false without providers")
	}
	if _, err := b.Prompt(context.Background(), "s", "q"); err == nil || !strings.Contains(err.Error(), "no LLM provider") {
		t.Fatalf("Prompt must return the actionable no-provider error, got %v", err)
	}
	if _, err := b.Agent(context.Background(), "s", "t", rpcserve.RunOpts{}); err == nil || !strings.Contains(err.Error(), "no LLM provider") {
		t.Fatalf("Agent must return the actionable no-provider error, got %v", err)
	}
}

func TestRPCBackend_PromptWithRouting(t *testing.T) {
	fc := &fakeClient{reply: "routed"}
	b := &rpcBackend{
		mgr:      &fakeManager{client: fc},
		provider: "OPENAI",
		sessions: map[string][]models.Message{},
	}
	out, err := b.PromptWith(context.Background(), "s1", "q", rpcserve.RunOpts{Provider: "DEVIN", Model: "gpt-5.6-sol"})
	if err != nil || out != "routed" {
		t.Fatalf("PromptWith: %q %v", out, err)
	}
	if len(b.sessions["s1"]) == 0 {
		t.Error("routed chat must still persist session history")
	}
	// No routing options: falls back to the plain Prompt path.
	if out, err := b.PromptWith(context.Background(), "s1", "q2", rpcserve.RunOpts{}); err != nil || out != "routed" {
		t.Fatalf("PromptWith default: %q %v", out, err)
	}
}

func TestRPCBackendPrompt(t *testing.T) {
	fc := &fakeClient{reply: "answer"}
	b := &rpcBackend{
		mgr:      &fakeManager{client: fc},
		sessions: map[string][]models.Message{},
	}

	out, err := b.Prompt(context.Background(), "s1", "hello")
	if err != nil || out != "answer" {
		t.Fatalf("prompt: %q %v", out, err)
	}
	// History is retained per session: a second turn sees the prior turns.
	if _, err := b.Prompt(context.Background(), "s1", "again"); err != nil {
		t.Fatal(err)
	}
	if fc.lastHist < 3 {
		t.Errorf("expected accumulated history (>=3), got %d", fc.lastHist)
	}
	// A different session starts fresh.
	if _, err := b.Prompt(context.Background(), "s2", "hi"); err != nil {
		t.Fatal(err)
	}
	if fc.lastHist != 1 {
		t.Errorf("new session should start with 1 message, got %d", fc.lastHist)
	}
}

func TestRPCBackendPrompt_HistoryCap(t *testing.T) {
	fc := &fakeClient{reply: "ok"}
	b := &rpcBackend{mgr: &fakeManager{client: fc}, sessions: map[string][]models.Message{}}
	for i := 0; i < rpcMaxHistory; i++ {
		if _, err := b.Prompt(context.Background(), "s", "msg"); err != nil {
			t.Fatal(err)
		}
	}
	b.mu.Lock()
	n := len(b.sessions["s"])
	b.mu.Unlock()
	if n > rpcMaxHistory {
		t.Errorf("history should be capped at %d, got %d", rpcMaxHistory, n)
	}
}
