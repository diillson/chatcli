package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/token"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// fakeClient satisfies client.LLMClient with provider/model tags so tests
// can assert which client was built.
type fakeClient struct {
	provider string
	model    string
}

func (f *fakeClient) GetModelName() string { return f.provider + "/" + f.model }
func (f *fakeClient) SendPrompt(ctx context.Context, prompt string, history []models.Message, maxTokens int) (string, error) {
	return "", nil
}

// minimalManager implements manager.LLMManager just enough for
// resolveSkillClient. Unused methods are no-ops.
type minimalManager struct {
	providers []string
	failFor   map[string]error
}

func (m *minimalManager) GetClient(provider, model string) (client.LLMClient, error) {
	if err, ok := m.failFor[provider]; ok {
		return nil, err
	}
	for _, p := range m.providers {
		if p == provider {
			return &fakeClient{provider: provider, model: model}, nil
		}
	}
	return nil, errors.New("provider not available: " + provider)
}

func (m *minimalManager) GetAvailableProviders() []string {
	out := append([]string(nil), m.providers...)
	return out
}

func (m *minimalManager) GetTokenManager() (token.Manager, bool) { return nil, false }
func (m *minimalManager) SetStackSpotRealm(string)               {}
func (m *minimalManager) SetStackSpotAgentID(string)             {}
func (m *minimalManager) GetStackSpotRealm() string              { return "" }
func (m *minimalManager) GetStackSpotAgentID() string            { return "" }
func (m *minimalManager) RefreshProviders()                      {}
func (m *minimalManager) CreateClientWithKey(provider, model, apiKey string) (client.LLMClient, error) {
	return m.GetClient(provider, model)
}
func (m *minimalManager) CreateClientWithConfig(provider, model, apiKey string, cfg map[string]string) (client.LLMClient, error) {
	return m.GetClient(provider, model)
}
func (m *minimalManager) ListModelsForProvider(ctx context.Context, provider string) ([]client.ModelInfo, error) {
	return nil, nil
}

func newResolverTestCLI(userProvider, userModel string, providers []string, cache []client.ModelInfo) *ChatCLI {
	mgr := &minimalManager{providers: providers}
	return &ChatCLI{
		logger:       zap.NewNop(),
		manager:      mgr,
		Provider:     userProvider,
		Model:        userModel,
		Client:       &fakeClient{provider: userProvider, model: userModel},
		cachedModels: cache,
	}
}

func TestResolveSkillClient_NoHint(t *testing.T) {
	cli := newResolverTestCLI("CLAUDEAI", "claude-sonnet-4-6", []string{"CLAUDEAI"}, nil)
	r := cli.resolveSkillClient("")
	if r.Changed {
		t.Fatal("empty hint must not change the client")
	}
	if r.Provider != "CLAUDEAI" || r.Model != "claude-sonnet-4-6" {
		t.Errorf("unexpected fallback: %+v", r)
	}
}

func TestResolveSkillClient_SameHintAsUser(t *testing.T) {
	cli := newResolverTestCLI("CLAUDEAI", "claude-sonnet-4-6", []string{"CLAUDEAI"}, nil)
	r := cli.resolveSkillClient("CLAUDE-SONNET-4-6")
	if r.Changed {
		t.Fatal("hint identical to user model must not swap")
	}
}

func TestResolveSkillClient_SameProviderSwap(t *testing.T) {
	cli := newResolverTestCLI("CLAUDEAI", "claude-sonnet-4-6",
		[]string{"CLAUDEAI"}, nil)
	r := cli.resolveSkillClient("claude-opus-4-5")
	if !r.Changed {
		t.Fatal("expected same-provider swap")
	}
	if r.CrossProvider {
		t.Error("same-provider swap should not set CrossProvider")
	}
	if r.Provider != "CLAUDEAI" || r.Model != "claude-opus-4-5" {
		t.Errorf("unexpected resolution: %+v", r)
	}
}

func TestResolveSkillClient_CrossProviderAvailable(t *testing.T) {
	cli := newResolverTestCLI("CLAUDEAI", "claude-sonnet-4-6",
		[]string{"CLAUDEAI", "OPENAI"}, nil)
	r := cli.resolveSkillClient("gpt-5")
	if !r.Changed {
		t.Fatalf("expected cross-provider swap, got fallback: %+v", r)
	}
	if !r.CrossProvider {
		t.Error("cross-provider swap must set CrossProvider=true")
	}
	if r.Provider != "OPENAI" || r.Model != "gpt-5" {
		t.Errorf("unexpected resolution: %+v", r)
	}
}

func TestResolveSkillClient_CrossProviderUnavailable(t *testing.T) {
	cli := newResolverTestCLI("CLAUDEAI", "claude-sonnet-4-6",
		[]string{"CLAUDEAI"}, nil)
	r := cli.resolveSkillClient("gpt-5")
	if r.Changed {
		t.Fatal("unavailable target provider must fall back, not swap")
	}
	if r.Note != "fallback-unavailable" {
		t.Errorf("expected fallback-unavailable note, got %q", r.Note)
	}
	if r.UserMessage == "" {
		t.Error("expected a human-readable UserMessage on fallback")
	}
	if r.Provider != "CLAUDEAI" || r.Model != "claude-sonnet-4-6" {
		t.Errorf("fallback should preserve user provider/model: %+v", r)
	}
}

func TestResolveSkillClient_APICacheHit(t *testing.T) {
	cache := []client.ModelInfo{
		{ID: "claude-sonnet-4-6-20251015-custom", Source: client.ModelSourceAPI},
	}
	cli := newResolverTestCLI("CLAUDEAI", "claude-sonnet-4-6",
		[]string{"CLAUDEAI"}, cache)
	r := cli.resolveSkillClient("claude-sonnet-4-6-20251015-custom")
	if !r.Changed {
		t.Fatal("expected same-provider swap via api cache")
	}
	if r.Note != "api-cached" {
		t.Errorf("expected note=api-cached, got %q", r.Note)
	}
	if r.Provider != "CLAUDEAI" {
		t.Error("api-cached hit must stay on user provider")
	}
}

func TestResolveSkillClient_FamilyHeuristicCross(t *testing.T) {
	cli := newResolverTestCLI("OPENAI", "gpt-5",
		[]string{"OPENAI", "CLAUDEAI"}, nil)
	r := cli.resolveSkillClient("claude-experimental-9000")
	if !r.Changed {
		t.Fatalf("expected family heuristic to swap, got %+v", r)
	}
	if r.Provider != "CLAUDEAI" {
		t.Errorf("family-cross-provider should pick CLAUDEAI, got %+v", r)
	}
}

func TestResolveSkillClient_OptimisticUnknown(t *testing.T) {
	cli := newResolverTestCLI("OPENAI", "gpt-5",
		[]string{"OPENAI"}, nil)
	r := cli.resolveSkillClient("mystery-model-xyz")
	if !r.Changed {
		t.Fatalf("expected optimistic swap, got fallback: %+v", r)
	}
	if r.Provider != "OPENAI" || r.Model != "mystery-model-xyz" {
		t.Errorf("unexpected resolution: %+v", r)
	}
	if r.Note != "optimistic-user-provider" {
		t.Errorf("expected optimistic note, got %q", r.Note)
	}
}
