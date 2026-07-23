package manager

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

// OPENROUTER_MODEL is documented on the site's configure-provider guide;
// an empty model must resolve through it (then the default) instead of
// silently pinning the default model.
func TestCreateClientWithKey_OpenRouterModelEnv(t *testing.T) {
	i18n.Init()
	setupTestEnv(t, map[string]string{})
	t.Setenv("OPENROUTER_MODEL", "moonshotai/kimi-k3")
	logger, _ := zap.NewDevelopment()
	mgr, err := NewLLMManager(logger)
	if err != nil {
		t.Fatalf("NewLLMManager: %v", err)
	}
	impl := mgr.(*LLMManagerImpl)
	defer impl.Close()

	cli, err := impl.CreateClientWithKey("OPENROUTER", "", "or-key")
	if err != nil {
		t.Fatalf("CreateClientWithKey: %v", err)
	}
	if name := cli.GetModelName(); !strings.Contains(name, "kimi-k3") {
		t.Fatalf("model name %q must reflect OPENROUTER_MODEL", name)
	}

	t.Setenv("OPENROUTER_MODEL", "")
	cli, err = impl.CreateClientWithKey("OPENROUTER", "", "or-key")
	if err != nil {
		t.Fatalf("CreateClientWithKey (default): %v", err)
	}
	if cli == nil {
		t.Fatal("nil client for default model")
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

// TestDevinCLIProvider covers the DEVIN provider wiring: registration is
// gated on the binary being present (via DEVIN_CLI_PATH here), the factory
// resolves the default model, and CreateClientWithConfig builds a client
// without any credential — auth belongs to the devin binary itself.
func TestDevinCLIProvider(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("script-based fake devin binary is unix-only")
	}
	i18n.Init()
	fake := filepath.Join(t.TempDir(), "devin")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho ok\n"), 0o700); err != nil {
		t.Fatalf("write fake devin: %v", err)
	}
	setupTestEnv(t, map[string]string{"DEVIN_CLI_PATH": fake})

	logger, _ := zap.NewDevelopment()
	mgr, err := NewLLMManager(logger)
	if err != nil {
		t.Fatalf("NewLLMManager: %v", err)
	}
	impl := mgr.(*LLMManagerImpl)
	defer impl.Close()

	found := false
	for _, p := range impl.GetAvailableProviders() {
		if p == "DEVIN" {
			found = true
		}
	}
	if !found {
		t.Fatalf("DEVIN must be listed when the binary resolves, got %v", impl.GetAvailableProviders())
	}

	cli, err := impl.GetClient("DEVIN", "")
	if err != nil {
		t.Fatalf("GetClient(DEVIN): %v", err)
	}
	if got := cli.GetModelName(); got == "" {
		t.Fatalf("default model must resolve to a display name")
	}

	viaCfg, err := impl.CreateClientWithConfig("DEVIN", "gpt-5.6-luna", "", nil)
	if err != nil {
		t.Fatalf("CreateClientWithConfig(DEVIN): %v", err)
	}
	if viaCfg == nil {
		t.Fatal("CreateClientWithConfig(DEVIN) returned nil client")
	}
}

// TestDevinCLIProvider_AbsentBinary pins the negative: no binary, no DEVIN
// in the provider list — same UX as a provider without credentials.
func TestDevinCLIProvider_AbsentBinary(t *testing.T) {
	i18n.Init()
	setupTestEnv(t, map[string]string{
		"DEVIN_CLI_PATH": filepath.Join(t.TempDir(), "missing"),
	})
	logger, _ := zap.NewDevelopment()
	mgr, err := NewLLMManager(logger)
	if err != nil {
		t.Fatalf("NewLLMManager: %v", err)
	}
	impl := mgr.(*LLMManagerImpl)
	defer impl.Close()
	for _, p := range impl.GetAvailableProviders() {
		if p == "DEVIN" {
			t.Fatal("DEVIN must not be listed when the binary is absent")
		}
	}
}
