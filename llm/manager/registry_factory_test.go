package manager

import (
	"testing"
	"time"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/registry"
	"go.uber.org/zap"
)

// TestRegistryFactories invokes each provider's registered Factory closure
// directly. The manager normally bypasses the registry (it owns the LLM
// instantiation contract for runtime), but each provider's init() still
// registers a Factory; this exercises those closures so the register.go
// files are not dead code from the unit-test perspective.
func TestRegistryFactories(t *testing.T) {
	i18n.Init()
	setupTestEnv(t, map[string]string{})

	logger, _ := zap.NewDevelopment()
	cfg := registry.ProviderConfig{
		APIKey:     "apikey:test",
		Logger:     logger,
		MaxRetries: 1,
		Backoff:    0,
	}

	cases := []string{
		"CLAUDEAI", "COPILOT", "GITHUB_MODELS", "GOOGLEAI",
		"MINIMAX", "MOONSHOT", "OPENAI", "OPENAI_RESPONSES",
		"OPENROUTER", "XAI", "ZAI",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			info, ok := registry.Get(name)
			if !ok {
				t.Skipf("provider %s not registered in this build", name)
				return
			}
			cli, err := info.Factory(cfg)
			if err != nil {
				t.Fatalf("Factory(%s): %v", name, err)
			}
			if cli == nil {
				t.Fatalf("Factory(%s) returned nil client", name)
			}
		})
	}
	_ = time.Millisecond // imported only for the test-env wiring; touch to avoid unused import
}
