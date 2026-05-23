package manager

import (
	"testing"

	"github.com/diillson/chatcli/i18n"
	"go.uber.org/zap"
)

// TestCreateClientWithKey_SupportedProviders exercises the per-provider
// branches of CreateClientWithKey, which is the boundary used by remote/server
// mode to instantiate a client from an inline token. The factory must wrap the
// raw key in a non-refreshing TokenProvider; we just need to confirm each
// branch returns a non-nil client without error.
func TestCreateClientWithKey_SupportedProviders(t *testing.T) {
	i18n.Init()
	setupTestEnv(t, map[string]string{})
	logger, _ := zap.NewDevelopment()
	mgr, err := NewLLMManager(logger)
	if err != nil {
		t.Fatalf("NewLLMManager: %v", err)
	}
	impl, ok := mgr.(*LLMManagerImpl)
	if !ok {
		t.Fatalf("manager is not *LLMManagerImpl")
	}
	defer impl.Close()

	cases := []struct{ provider, key, model string }{
		{"OPENAI", "apikey:sk-test", "gpt-4o"},
		{"OPENAI", "oauth:eyJabc", "gpt-5"}, // OAuth routes to Responses API
		{"CLAUDEAI", "apikey:sk-ant", "claude-sonnet-4-5"},
		{"GOOGLEAI", "g-key", "gemini-pro"},
		{"XAI", "x-key", "grok-4"},
		{"ZAI", "z-key", "glm-4.7"},
		{"MINIMAX", "mm-key", "MiniMax-M2.7"},
		{"MOONSHOT", "ms-key", "kimi-k2.6"},
		{"COPILOT", "token:gho_test", "gpt-4o"},
		{"OPENROUTER", "or-key", "anthropic/claude-3-haiku"},
	}
	for _, c := range cases {
		t.Run(c.provider, func(t *testing.T) {
			cli, err := impl.CreateClientWithKey(c.provider, c.model, c.key)
			if err != nil {
				t.Fatalf("CreateClientWithKey(%s): %v", c.provider, err)
			}
			if cli == nil {
				t.Fatalf("CreateClientWithKey(%s) returned nil client", c.provider)
			}
		})
	}
}

func TestCreateClientWithKey_UnsupportedProvider(t *testing.T) {
	i18n.Init()
	setupTestEnv(t, map[string]string{})
	logger, _ := zap.NewDevelopment()
	mgr, _ := NewLLMManager(logger)
	impl := mgr.(*LLMManagerImpl)
	defer impl.Close()

	if _, err := impl.CreateClientWithKey("NONEXISTENT", "model", "key"); err == nil {
		t.Fatal("expected error for unsupported provider")
	}
}

func TestManager_TokenProviderLifecycle(t *testing.T) {
	i18n.Init()
	setupTestEnv(t, map[string]string{"ANTHROPIC_API_KEY": "sk-test"})
	logger, _ := zap.NewDevelopment()
	mgr, err := NewLLMManager(logger)
	if err != nil {
		t.Fatalf("NewLLMManager: %v", err)
	}
	impl := mgr.(*LLMManagerImpl)

	// First resolve seeds the cache.
	if _, _, err := impl.tokenProviderForTestable(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// RefreshProviders should close and clear the cache without panic.
	impl.RefreshProviders()
	if _, _, err := impl.tokenProviderForTestable(); err != nil {
		t.Fatalf("after refresh: %v", err)
	}
	// Close releases everything; calling again must be idempotent.
	impl.Close()
	impl.Close()
}

// tokenProviderForTestable is a tiny shim exposing the cached provider count
// alongside the resolved provider for the lifecycle test.
func (m *LLMManagerImpl) tokenProviderForTestable() (interface{}, int, error) {
	tp, err := m.tokenProviderFor("anthropic")
	m.tpMu.Lock()
	n := len(m.tokenProviders)
	m.tpMu.Unlock()
	return tp, n, err
}
