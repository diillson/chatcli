/*
 * ChatCLI - @model tool adapter tests
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/token"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// routingStubClient is a minimal LLMClient whose model name reveals which
// client the resolver built.
type routingStubClient struct {
	model  string
	answer string
}

func (c *routingStubClient) GetModelName() string { return c.model }

func (c *routingStubClient) SendPrompt(context.Context, string, []models.Message, int) (string, error) {
	return c.answer, nil
}

// fakeRoutingManager satisfies manager.LLMManager for adapter tests.
type fakeRoutingManager struct {
	providers []string
	models    map[string][]client.ModelInfo
	built     []string // "PROVIDER|model"
}

func (f *fakeRoutingManager) GetClient(provider, model string) (client.LLMClient, error) {
	f.built = append(f.built, provider+"|"+model)
	return &routingStubClient{model: model, answer: "stub answer"}, nil
}

func (f *fakeRoutingManager) GetAvailableProviders() []string        { return f.providers }
func (f *fakeRoutingManager) GetTokenManager() (token.Manager, bool) { return nil, false }
func (f *fakeRoutingManager) SetStackSpotRealm(string)               {}
func (f *fakeRoutingManager) SetStackSpotAgentID(string)             {}
func (f *fakeRoutingManager) GetStackSpotRealm() string              { return "" }
func (f *fakeRoutingManager) GetStackSpotAgentID() string            { return "" }
func (f *fakeRoutingManager) RefreshProviders()                      {}

func (f *fakeRoutingManager) CreateClientWithKey(string, string, string) (client.LLMClient, error) {
	return nil, errors.New("not implemented")
}

func (f *fakeRoutingManager) CreateClientWithConfig(string, string, string, map[string]string) (client.LLMClient, error) {
	return nil, errors.New("not implemented")
}

func (f *fakeRoutingManager) ListModelsForProvider(_ context.Context, provider string) ([]client.ModelInfo, error) {
	if ms, ok := f.models[provider]; ok {
		return ms, nil
	}
	return nil, errors.New("no models")
}

func newRoutingTestCLI() (*ChatCLI, *fakeRoutingManager) {
	mgr := &fakeRoutingManager{
		providers: []string{"CLAUDEAI", "GOOGLEAI"},
		models: map[string][]client.ModelInfo{
			"CLAUDEAI": {
				{ID: "claude-sonnet-5", Source: client.ModelSourceAPI},
				{ID: "claude-haiku-4-5-20251001", Source: client.ModelSourceAPI},
			},
			"GOOGLEAI": {
				{ID: "gemini-2.5-flash", Source: client.ModelSourceCatalog},
			},
		},
	}
	cli := &ChatCLI{
		manager:     mgr,
		Provider:    "CLAUDEAI",
		Model:       "claude-sonnet-5",
		Client:      &routingStubClient{model: "claude-sonnet-5", answer: "session answer"},
		logger:      zap.NewNop(),
		costTracker: NewCostTracker(),
	}
	return cli, mgr
}

func TestModelRoutingTierDerivation(t *testing.T) {
	tests := []struct {
		provider, model, wantTier string
	}{
		{"CLAUDEAI", "claude-haiku-4-5-20251001", "fast-cheap"},
		{"GOOGLEAI", "gemini-2.5-flash", "fast-cheap"},
		{"CLAUDEAI", "claude-sonnet-5", "balanced"},
		{"CLAUDEAI", "claude-fable-5", "frontier"},
		{"OPENAI", "o3", "frontier"},
		{"OLLAMA", "qwen2.5:14b", "unmetered"},
	}
	for _, tc := range tests {
		if tier, _, _ := modelRoutingTier(tc.provider, tc.model); tier != tc.wantTier {
			t.Errorf("modelRoutingTier(%s, %s) = %q, want %q", tc.provider, tc.model, tier, tc.wantTier)
		}
	}
}

func TestModelToolUseResetStatus(t *testing.T) {
	cliObj, _ := newRoutingTestCLI()
	adapter := &modelRoutingAdapter{cli: cliObj}
	ctx := context.Background()

	t.Run("qualified cross-provider use sets override", func(t *testing.T) {
		out, err := adapter.Use(ctx, "GOOGLEAI:gemini-2.5-flash")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := cliObj.agentRouteOverrideHandle(); got != "GOOGLEAI:gemini-2.5-flash" {
			t.Errorf("override = %q, want GOOGLEAI:gemini-2.5-flash", got)
		}
		if !strings.Contains(out, "GOOGLEAI:gemini-2.5-flash") {
			t.Errorf("result must echo the routed handle, got %q", out)
		}
	})

	t.Run("status reports override", func(t *testing.T) {
		out, err := adapter.Status()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(out, "GOOGLEAI:gemini-2.5-flash") {
			t.Errorf("status must show the override, got %q", out)
		}
	})

	t.Run("unconfigured provider is an actionable error", func(t *testing.T) {
		if _, err := adapter.Use(ctx, "XAI:grok-4"); err == nil {
			t.Fatal("expected error for a provider without credentials")
		} else if !strings.Contains(err.Error(), "XAI") {
			t.Errorf("error must name the refused provider, got %q", err.Error())
		}
		// A failed use must not clobber the previous override.
		if got := cliObj.agentRouteOverrideHandle(); got != "GOOGLEAI:gemini-2.5-flash" {
			t.Errorf("failed use clobbered the override: %q", got)
		}
	})

	t.Run("using the session model clears the override", func(t *testing.T) {
		if _, err := adapter.Use(ctx, "CLAUDEAI:claude-sonnet-5"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := cliObj.agentRouteOverrideHandle(); got != "" {
			t.Errorf("override should be cleared, got %q", got)
		}
	})

	t.Run("reset clears and reports", func(t *testing.T) {
		cliObj.setAgentRouteOverride("GOOGLEAI:gemini-2.5-flash")
		if _, err := adapter.Reset(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := cliObj.agentRouteOverrideHandle(); got != "" {
			t.Errorf("override should be cleared, got %q", got)
		}
	})
}

func TestModelToolDelegate(t *testing.T) {
	cliObj, _ := newRoutingTestCLI()
	adapter := &modelRoutingAdapter{cli: cliObj}

	out, err := adapter.Delegate(context.Background(), "GOOGLEAI:gemini-2.5-flash", "summarize this", 256)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "stub answer") {
		t.Errorf("delegate must return the delegated model's answer, got %q", out)
	}
	if got := cliObj.agentRouteOverrideHandle(); got != "" {
		t.Errorf("delegate must NOT set a route override, got %q", got)
	}
	if cliObj.Client.GetModelName() != "claude-sonnet-5" {
		t.Error("delegate must not touch the session client")
	}
	// Cost is attributed to the delegated model.
	cliObj.costTracker.mu.RLock()
	_, recorded := cliObj.costTracker.modelUsage[modelKey("GOOGLEAI", "gemini-2.5-flash")]
	cliObj.costTracker.mu.RUnlock()
	if !recorded {
		t.Error("delegate usage must be recorded under the delegated provider:model")
	}
}

func TestModelToolList(t *testing.T) {
	cliObj, _ := newRoutingTestCLI()
	adapter := &modelRoutingAdapter{cli: cliObj}

	t.Run("all providers", func(t *testing.T) {
		out, err := adapter.List(context.Background(), "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		for _, want := range []string{
			"CLAUDEAI:claude-sonnet-5",
			"CLAUDEAI:claude-haiku-4-5-20251001",
			"GOOGLEAI:gemini-2.5-flash",
			"tier=fast-cheap",
			"tier=balanced",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("listing missing %q:\n%s", want, out)
			}
		}
	})

	t.Run("provider filter", func(t *testing.T) {
		out, err := adapter.List(context.Background(), "googleai")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Contains(out, "CLAUDEAI (") {
			t.Errorf("filtered listing must not include other providers:\n%s", out)
		}
	})

	t.Run("unknown filter is an error", func(t *testing.T) {
		if _, err := adapter.List(context.Background(), "NOPE"); err == nil {
			t.Fatal("expected error for unknown provider filter")
		}
	})
}

func TestClientAndCtxForTurnPrefersRouteOverride(t *testing.T) {
	cliObj, _ := newRoutingTestCLI()
	a := &AgentMode{cli: cliObj, logger: zap.NewNop()}
	ctx := context.Background()

	// No hints: session client.
	turnClient, _ := a.clientAndCtxForTurn(ctx)
	if turnClient.GetModelName() != "claude-sonnet-5" {
		t.Fatalf("baseline turn client = %s, want session model", turnClient.GetModelName())
	}

	// Skill hint alone is honored.
	a.skillModelHint = "claude-haiku-4-5-20251001"
	turnClient, _ = a.clientAndCtxForTurn(ctx)
	if turnClient.GetModelName() != "claude-haiku-4-5-20251001" {
		t.Fatalf("skill hint not honored, got %s", turnClient.GetModelName())
	}

	// @model override outranks the skill hint.
	cliObj.setAgentRouteOverride("GOOGLEAI:gemini-2.5-flash")
	turnClient, _ = a.clientAndCtxForTurn(ctx)
	if turnClient.GetModelName() != "gemini-2.5-flash" {
		t.Fatalf("route override must win over skill hint, got %s", turnClient.GetModelName())
	}

	// Reset returns to the skill hint.
	cliObj.clearAgentRouteOverride()
	turnClient, _ = a.clientAndCtxForTurn(ctx)
	if turnClient.GetModelName() != "claude-haiku-4-5-20251001" {
		t.Fatalf("after reset expected skill hint client, got %s", turnClient.GetModelName())
	}
}

// TestModelToolEndToEndSwitch exercises the full chain the agent uses at
// runtime: the @model plugin call goes through the wired adapter, flips the
// route override, and the NEXT turn's client (clientAndCtxForTurn) actually
// serves the routed model — then reset returns to the session client. This
// is the mid-task cross-provider switch scenario end-to-end, minus the LLM.
func TestModelToolEndToEndSwitch(t *testing.T) {
	cliObj, _ := newRoutingTestCLI()
	plugins.SetModelRoutingAdapter(&modelRoutingAdapter{cli: cliObj})
	t.Cleanup(func() { plugins.SetModelRoutingAdapter(nil) })

	p := plugins.NewBuiltinModelPlugin()
	a := &AgentMode{cli: cliObj, logger: zap.NewNop()}
	ctx := context.Background()

	// Turn 1: agent asks to route the task to another provider's model.
	if _, err := p.Execute(ctx, []string{`{"cmd":"use","args":{"model":"GOOGLEAI:gemini-2.5-flash"}}`}); err != nil {
		t.Fatalf("@model use failed: %v", err)
	}

	// Turn 2: the loop resolves the turn client — must be the routed model,
	// on the other provider, without mutating the session's own state.
	turnClient, _ := a.clientAndCtxForTurn(ctx)
	if turnClient.GetModelName() != "gemini-2.5-flash" {
		t.Fatalf("turn client = %s, want the routed gemini-2.5-flash", turnClient.GetModelName())
	}
	if cliObj.Provider != "CLAUDEAI" || cliObj.Model != "claude-sonnet-5" {
		t.Fatalf("session state mutated: %s/%s", cliObj.Provider, cliObj.Model)
	}

	// Turn 3: agent resets — the next turn is back on the session client.
	if _, err := p.Execute(ctx, []string{`{"cmd":"reset"}`}); err != nil {
		t.Fatalf("@model reset failed: %v", err)
	}
	turnClient, _ = a.clientAndCtxForTurn(ctx)
	if turnClient.GetModelName() != "claude-sonnet-5" {
		t.Fatalf("after reset turn client = %s, want the session model", turnClient.GetModelName())
	}
}

// effectiveRoute is what the squad dispatcher and subagent delegation follow:
// it must track the AI's @model override and fall back to the session pair.
func TestEffectiveRouteFollowsModelOverride(t *testing.T) {
	cliObj, _ := newRoutingTestCLI()
	plugins.SetModelRoutingAdapter(&modelRoutingAdapter{cli: cliObj})
	t.Cleanup(func() { plugins.SetModelRoutingAdapter(nil) })
	p := plugins.NewBuiltinModelPlugin()
	a := &AgentMode{cli: cliObj, logger: zap.NewNop()}
	ctx := context.Background()

	if prov, model := a.effectiveRoute(); prov != "CLAUDEAI" || model != "claude-sonnet-5" {
		t.Fatalf("baseline route = %s/%s, want the session pair", prov, model)
	}
	if _, err := p.Execute(ctx, []string{`{"cmd":"use","args":{"model":"GOOGLEAI:gemini-2.5-flash"}}`}); err != nil {
		t.Fatalf("@model use failed: %v", err)
	}
	if prov, model := a.effectiveRoute(); prov != "GOOGLEAI" || model != "gemini-2.5-flash" {
		t.Fatalf("route after @model use = %s/%s, want GOOGLEAI/gemini-2.5-flash", prov, model)
	}
	if _, err := p.Execute(ctx, []string{`{"cmd":"reset"}`}); err != nil {
		t.Fatalf("@model reset failed: %v", err)
	}
	if prov, model := a.effectiveRoute(); prov != "CLAUDEAI" || model != "claude-sonnet-5" {
		t.Fatalf("route after reset = %s/%s, want the session pair", prov, model)
	}
}
