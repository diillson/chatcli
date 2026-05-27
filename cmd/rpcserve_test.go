package cmd

import (
	"context"
	"testing"

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
	client *fakeClient
}

func (m *fakeManager) GetClient(string, string) (client.LLMClient, error) { return m.client, nil }

func TestFirstNonEmpty(t *testing.T) {
	if firstNonEmpty("", "", "x", "y") != "x" {
		t.Error("should return first non-empty")
	}
	if firstNonEmpty("", "") != "" {
		t.Error("all empty -> empty")
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
