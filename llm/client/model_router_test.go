/*
 * ChatCLI - Model router tests
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Covers the resolution pipeline's explicit "PROVIDER:model" qualified-hint
 * stage and the KnownProviders registry completeness (every provider the
 * manager can register must be discoverable by CatalogProviderOf, or bare
 * hints can never route cross-provider to it).
 */
package client

import (
	"strings"
	"testing"
)

// fakeRouter satisfies ProviderRouter for resolver tests, recording every
// GetClient call so assertions can verify which provider/model was built.
type fakeRouter struct {
	available []string
	calls     []string // "PROVIDER|model"
}

func (f *fakeRouter) GetClient(provider, model string) (LLMClient, error) {
	f.calls = append(f.calls, provider+"|"+model)
	return &MockLLMClient{}, nil
}

func (f *fakeRouter) GetAvailableProviders() []string { return f.available }

// TestKnownProvidersCoversManagerRegistry pins the full canonical provider
// list. BEDROCK, MOONSHOT and DEVIN were registered in the manager after this
// list was written and were missing from it — which silently disabled
// cross-provider routing to them for bare (unqualified) hints.
func TestKnownProvidersCoversManagerRegistry(t *testing.T) {
	required := []string{
		"OPENAI", "OPENAI_ASSISTANT", "CLAUDEAI", "BEDROCK", "GOOGLEAI",
		"XAI", "ZAI", "MOONSHOT", "MINIMAX", "STACKSPOT", "OLLAMA",
		"COPILOT", "GITHUB_MODELS", "OPENROUTER", "DEVIN",
	}
	for _, p := range required {
		if !ContainsProvider(KnownProviders, p) {
			t.Errorf("KnownProviders is missing %q — CatalogProviderOf can never route bare hints to it", p)
		}
	}
}

// TestCatalogProviderOfBedrockPrecedence verifies that models shared between
// the direct Anthropic API and Bedrock keep resolving to CLAUDEAI (direct API
// wins the KnownProviders order), so adding BEDROCK to the list does not
// change existing bare-hint behavior.
func TestCatalogProviderOfBedrockPrecedence(t *testing.T) {
	if got := CatalogProviderOf("claude-sonnet-5"); got != "CLAUDEAI" {
		t.Errorf("CatalogProviderOf(claude-sonnet-5) = %q, want CLAUDEAI (direct API must win over Bedrock mirrors)", got)
	}
}

func TestSplitQualifiedHint(t *testing.T) {
	available := []string{"CLAUDEAI", "OLLAMA"}
	tests := []struct {
		name         string
		hint         string
		wantProvider string
		wantModel    string
		wantOK       bool
	}{
		{"qualified known provider", "BEDROCK:anthropic.claude-haiku-4-5", "BEDROCK", "anthropic.claude-haiku-4-5", true},
		{"qualified case-insensitive", "claudeai:claude-haiku-4-5-20251001", "CLAUDEAI", "claude-haiku-4-5-20251001", true},
		{"ollama tag is not a provider prefix", "qwen2.5:14b", "", "", false},
		{"qualified ollama keeps tag colon", "OLLAMA:qwen2.5:14b", "OLLAMA", "qwen2.5:14b", true},
		{"no colon", "claude-haiku-4-5", "", "", false},
		{"empty model part", "CLAUDEAI:", "", "", false},
		{"empty provider part", ":claude-haiku-4-5", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			provider, model, ok := SplitQualifiedHint(tc.hint, available)
			if provider != tc.wantProvider || model != tc.wantModel || ok != tc.wantOK {
				t.Errorf("SplitQualifiedHint(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tc.hint, provider, model, ok, tc.wantProvider, tc.wantModel, tc.wantOK)
			}
		})
	}
}

func TestResolveModelRoutingQualifiedHint(t *testing.T) {
	t.Run("explicit cross-provider", func(t *testing.T) {
		router := &fakeRouter{available: []string{"CLAUDEAI", "BEDROCK"}}
		r := ResolveModelRouting(ResolveModelRoutingInput{
			Router:       router,
			UserProvider: "CLAUDEAI",
			UserModel:    "claude-sonnet-5",
			UserClient:   &MockLLMClient{},
			Hint:         "BEDROCK:anthropic.claude-haiku-4-5",
		})
		if !r.Changed || !r.CrossProvider {
			t.Fatalf("expected cross-provider change, got Changed=%v CrossProvider=%v Note=%q", r.Changed, r.CrossProvider, r.Note)
		}
		if r.Provider != "BEDROCK" || r.Model != "anthropic.claude-haiku-4-5" {
			t.Errorf("resolved to %s/%s, want BEDROCK/anthropic.claude-haiku-4-5", r.Provider, r.Model)
		}
		if r.Note != "explicit-provider" {
			t.Errorf("Note = %q, want explicit-provider", r.Note)
		}
		if len(router.calls) != 1 || router.calls[0] != "BEDROCK|anthropic.claude-haiku-4-5" {
			t.Errorf("GetClient calls = %v, want exactly [BEDROCK|anthropic.claude-haiku-4-5]", router.calls)
		}
	})

	t.Run("explicit same provider strips prefix", func(t *testing.T) {
		router := &fakeRouter{available: []string{"CLAUDEAI"}}
		r := ResolveModelRouting(ResolveModelRoutingInput{
			Router:       router,
			UserProvider: "CLAUDEAI",
			UserModel:    "claude-sonnet-5",
			UserClient:   &MockLLMClient{},
			Hint:         "claudeai:claude-haiku-4-5-20251001",
		})
		if !r.Changed || r.CrossProvider {
			t.Fatalf("expected same-provider change, got Changed=%v CrossProvider=%v Note=%q", r.Changed, r.CrossProvider, r.Note)
		}
		if r.Model != "claude-haiku-4-5-20251001" {
			t.Errorf("Model = %q, want the hint without the provider prefix", r.Model)
		}
	})

	t.Run("explicit same provider same model is a no-op", func(t *testing.T) {
		router := &fakeRouter{available: []string{"CLAUDEAI"}}
		r := ResolveModelRouting(ResolveModelRoutingInput{
			Router:       router,
			UserProvider: "CLAUDEAI",
			UserModel:    "claude-sonnet-5",
			UserClient:   &MockLLMClient{},
			Hint:         "CLAUDEAI:claude-sonnet-5",
		})
		if r.Changed {
			t.Errorf("expected no change, got Changed=true Note=%q", r.Note)
		}
	})

	t.Run("explicit provider not configured falls back with message", func(t *testing.T) {
		router := &fakeRouter{available: []string{"CLAUDEAI"}}
		r := ResolveModelRouting(ResolveModelRoutingInput{
			Router:       router,
			UserProvider: "CLAUDEAI",
			UserModel:    "claude-sonnet-5",
			UserClient:   &MockLLMClient{},
			Hint:         "BEDROCK:anthropic.claude-haiku-4-5",
		})
		if r.Changed {
			t.Fatalf("expected fallback, got Changed=true")
		}
		if r.Note != "fallback-unavailable" {
			t.Errorf("Note = %q, want fallback-unavailable", r.Note)
		}
		if strings.TrimSpace(r.UserMessage) == "" {
			t.Error("UserMessage must explain why the explicit provider was not honored")
		}
	})

	t.Run("bare ollama tag with colon is not treated as qualified", func(t *testing.T) {
		router := &fakeRouter{available: []string{"OLLAMA"}}
		r := ResolveModelRouting(ResolveModelRoutingInput{
			Router:       router,
			UserProvider: "OLLAMA",
			UserModel:    "llama3",
			UserClient:   &MockLLMClient{},
			Hint:         "qwen2.5:14b",
		})
		if !r.Changed {
			t.Fatalf("expected the family heuristic to honor the hint on OLLAMA, got Note=%q", r.Note)
		}
		if r.Model != "qwen2.5:14b" {
			t.Errorf("Model = %q, want the full ollama tag qwen2.5:14b preserved", r.Model)
		}
		if r.CrossProvider {
			t.Error("qwen on OLLAMA must stay same-provider")
		}
	})
}
