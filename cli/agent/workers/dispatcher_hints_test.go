package workers

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/token"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// hintTrackingManager is a test LLMManager that records every GetClient
// call so tests can verify which (provider, model) the dispatcher asked
// for. It also reports configurable available providers so cross-provider
// fallback branches can be exercised.
type hintTrackingManager struct {
	mu           sync.Mutex
	providers    []string
	getClientLog []struct{ Provider, Model string }
}

func (m *hintTrackingManager) GetClient(provider, model string) (client.LLMClient, error) {
	m.mu.Lock()
	m.getClientLog = append(m.getClientLog, struct{ Provider, Model string }{provider, model})
	m.mu.Unlock()
	// Only honor requests for providers we advertise as available.
	for _, p := range m.providers {
		if p == provider {
			return &hintTrackingClient{provider: provider, model: model}, nil
		}
	}
	return nil, fmt.Errorf("provider not available: %s", provider)
}
func (m *hintTrackingManager) GetAvailableProviders() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := append([]string(nil), m.providers...)
	return out
}

// hintTrackingClient captures the ctx it receives in SendPrompt so tests
// can assert that effort hints were attached.
type hintTrackingClient struct {
	provider        string
	model           string
	lastEffort      client.SkillEffort
	sendPromptCalls int
	mu              sync.Mutex
}

func (c *hintTrackingClient) GetModelName() string { return c.provider + "/" + c.model }
func (c *hintTrackingClient) SendPrompt(ctx context.Context, prompt string, history []models.Message, maxTokens int) (string, error) {
	c.mu.Lock()
	c.sendPromptCalls++
	c.lastEffort = client.EffortFromContext(ctx)
	c.mu.Unlock()
	return "ok", nil
}

// hintedMockAgent overrides mockAgent to return configurable Model/Effort
// hints. It also captures the WorkerDeps.LLMClient and ctx it was called
// with so tests can verify the dispatcher honored the hints.
type hintedMockAgent struct {
	mockAgent
	modelHint  string
	effortHint string

	// Captured on Execute.
	mu         sync.Mutex
	seenClient client.LLMClient
	seenEffort client.SkillEffort
}

func (a *hintedMockAgent) Model() string  { return a.modelHint }
func (a *hintedMockAgent) Effort() string { return a.effortHint }
func (a *hintedMockAgent) Execute(ctx context.Context, task string, deps *WorkerDeps) (*AgentResult, error) {
	a.mu.Lock()
	a.seenClient = deps.LLMClient
	a.seenEffort = client.EffortFromContext(ctx)
	a.mu.Unlock()
	return &AgentResult{Output: "ok"}, nil
}

// fullMockMgr implements manager.LLMManager with the methods needed by
// the dispatcher, delegating GetClient to hintTrackingManager so tests can
// inspect the call log.
type fullMockMgr struct{ *hintTrackingManager }

func (f *fullMockMgr) SetStackSpotRealm(string)               {}
func (f *fullMockMgr) SetStackSpotAgentID(string)             {}
func (f *fullMockMgr) GetStackSpotRealm() string              { return "" }
func (f *fullMockMgr) GetStackSpotAgentID() string            { return "" }
func (f *fullMockMgr) RefreshProviders()                      {}
func (f *fullMockMgr) GetTokenManager() (token.Manager, bool) { return nil, false }
func (f *fullMockMgr) CreateClientWithKey(p, m, k string) (client.LLMClient, error) {
	return f.GetClient(p, m)
}
func (f *fullMockMgr) CreateClientWithConfig(p, m, k string, _ map[string]string) (client.LLMClient, error) {
	return f.GetClient(p, m)
}
func (f *fullMockMgr) ListModelsForProvider(context.Context, string) ([]client.ModelInfo, error) {
	return nil, nil
}

func TestDispatcher_ModelHintSameProvider(t *testing.T) {
	mgr := &fullMockMgr{hintTrackingManager: &hintTrackingManager{providers: []string{"CLAUDEAI"}}}

	registry := NewRegistry()
	agent := &hintedMockAgent{
		mockAgent: mockAgent{agentType: AgentTypePlanner},
		modelHint: "claude-opus-4-6",
	}
	registry.Register(agent)

	dispatcher := NewDispatcher(registry, mgr, DispatcherConfig{
		MaxWorkers:    1,
		ParallelMode:  false,
		Provider:      "CLAUDEAI",
		Model:         "claude-sonnet-4-6",
		WorkerTimeout: 5 * time.Second,
	}, zap.NewNop())

	results := dispatcher.Dispatch(context.Background(), []AgentCall{{
		Agent: AgentTypePlanner, Task: "plan this", ID: "c1",
	}})
	if len(results) != 1 || results[0].Error != nil {
		t.Fatalf("unexpected results: %+v", results)
	}

	// The dispatcher should have asked for the hinted model on the same
	// provider. We can't easily assert the exact GetClient call sequence
	// because the resolver may call it twice (first to build, then fail-
	// back if the builder errors), but at a minimum the hinted model
	// should appear.
	found := false
	mgr.mu.Lock()
	for _, call := range mgr.getClientLog {
		if call.Provider == "CLAUDEAI" && call.Model == "claude-opus-4-6" {
			found = true
			break
		}
	}
	mgr.mu.Unlock()
	if !found {
		t.Fatalf("expected dispatcher to request claude-opus-4-6; log=%+v", mgr.getClientLog)
	}
}

func TestDispatcher_ModelHintCrossProviderAvailable(t *testing.T) {
	mgr := &fullMockMgr{hintTrackingManager: &hintTrackingManager{providers: []string{"CLAUDEAI", "OPENAI"}}}

	registry := NewRegistry()
	agent := &hintedMockAgent{
		mockAgent: mockAgent{agentType: AgentTypeReviewer},
		modelHint: "gpt-5",
	}
	registry.Register(agent)

	dispatcher := NewDispatcher(registry, mgr, DispatcherConfig{
		MaxWorkers:    1,
		ParallelMode:  false,
		Provider:      "CLAUDEAI",
		Model:         "claude-sonnet-4-6",
		WorkerTimeout: 5 * time.Second,
	}, zap.NewNop())

	results := dispatcher.Dispatch(context.Background(), []AgentCall{{
		Agent: AgentTypeReviewer, Task: "review this", ID: "c1",
	}})
	if len(results) != 1 || results[0].Error != nil {
		t.Fatalf("unexpected results: %+v", results)
	}

	// Expect a cross-provider swap to OPENAI with gpt-5.
	found := false
	mgr.mu.Lock()
	for _, call := range mgr.getClientLog {
		if call.Provider == "OPENAI" && call.Model == "gpt-5" {
			found = true
			break
		}
	}
	mgr.mu.Unlock()
	if !found {
		t.Fatalf("expected dispatcher to swap to OPENAI/gpt-5; log=%+v", mgr.getClientLog)
	}
}

func TestDispatcher_ModelHintCrossProviderUnavailable(t *testing.T) {
	// OPENAI NOT configured — the resolver must fall back to the dispatcher
	// default without failing the agent call.
	mgr := &fullMockMgr{hintTrackingManager: &hintTrackingManager{providers: []string{"CLAUDEAI"}}}

	registry := NewRegistry()
	agent := &hintedMockAgent{
		mockAgent: mockAgent{agentType: AgentTypeReviewer},
		modelHint: "gpt-5",
	}
	registry.Register(agent)

	dispatcher := NewDispatcher(registry, mgr, DispatcherConfig{
		MaxWorkers:    1,
		ParallelMode:  false,
		Provider:      "CLAUDEAI",
		Model:         "claude-sonnet-4-6",
		WorkerTimeout: 5 * time.Second,
	}, zap.NewNop())

	results := dispatcher.Dispatch(context.Background(), []AgentCall{{
		Agent: AgentTypeReviewer, Task: "review this", ID: "c1",
	}})
	if len(results) != 1 || results[0].Error != nil {
		t.Fatalf("unexpected results (should gracefully fall back): %+v", results)
	}

	// The final GetClient call should target CLAUDEAI (the user's default),
	// proving the dispatcher fell back instead of erroring out.
	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	lastCall := mgr.getClientLog[len(mgr.getClientLog)-1]
	if lastCall.Provider != "CLAUDEAI" || lastCall.Model != "claude-sonnet-4-6" {
		t.Fatalf("expected graceful fallback to CLAUDEAI/claude-sonnet-4-6; last call=%+v", lastCall)
	}
}

func TestDispatcher_EffortHintAttachedToCtx(t *testing.T) {
	mgr := &fullMockMgr{hintTrackingManager: &hintTrackingManager{providers: []string{"CLAUDEAI"}}}

	registry := NewRegistry()
	agent := &hintedMockAgent{
		mockAgent:  mockAgent{agentType: AgentTypePlanner},
		effortHint: "high",
	}
	registry.Register(agent)

	dispatcher := NewDispatcher(registry, mgr, DispatcherConfig{
		MaxWorkers:    1,
		ParallelMode:  false,
		Provider:      "CLAUDEAI",
		Model:         "claude-sonnet-4-6",
		WorkerTimeout: 5 * time.Second,
	}, zap.NewNop())

	results := dispatcher.Dispatch(context.Background(), []AgentCall{{
		Agent: AgentTypePlanner, Task: "plan this", ID: "c1",
	}})
	if len(results) != 1 || results[0].Error != nil {
		t.Fatalf("unexpected results: %+v", results)
	}

	agent.mu.Lock()
	defer agent.mu.Unlock()
	if agent.seenEffort != client.EffortHigh {
		t.Fatalf("expected agent to see EffortHigh on ctx, got %q", agent.seenEffort)
	}
}
