/*
 * ChatCLI - Tests for the RunServer helpers
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Covers the two helpers extracted from RunServer (initFallbackChain,
 * initMCPManager) so the cmd package has a real coverage floor and so
 * the diff-coverage gate stops being dominated by the 0% on this file.
 *
 * Both helpers are tested through narrow interfaces (fallbackChainSink,
 * mcpSink) instead of the full *server.Server so a unit test can
 * assert the wiring without standing up a real gRPC stack.
 */
package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/fallback"
	"github.com/diillson/chatcli/llm/manager"
	"github.com/diillson/chatcli/llm/token"
	"github.com/diillson/chatcli/cli/mcp"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// --- fallbackChainSink / mcpSink doubles ---------------------------------

type fakeFallbackSink struct {
	chain *fallback.Chain
}

func (f *fakeFallbackSink) SetFallbackChain(c *fallback.Chain) { f.chain = c }

type fakeMCPSink struct {
	mgr *mcp.Manager
}

func (f *fakeMCPSink) SetMCPManager(m *mcp.Manager) { f.mgr = m }

// --- LLMManager double ---------------------------------------------------

// fakeLLMManager satisfies manager.LLMManager. Only GetClient carries
// real behavior — the rest are no-ops because the helper under test
// only calls GetClient. Keeps the test focused.
type fakeLLMManager struct {
	clients map[string]client.LLMClient
	errors  map[string]error
	calls   atomic.Int32
}

func (f *fakeLLMManager) GetClient(provider, _ string) (client.LLMClient, error) {
	f.calls.Add(1)
	if err, ok := f.errors[provider]; ok {
		return nil, err
	}
	if c, ok := f.clients[provider]; ok {
		return c, nil
	}
	return nil, errors.New("unknown provider")
}
func (f *fakeLLMManager) GetAvailableProviders() []string         { return nil }
func (f *fakeLLMManager) GetTokenManager() (token.Manager, bool)  { return nil, false }
func (f *fakeLLMManager) SetStackSpotRealm(string)                {}
func (f *fakeLLMManager) SetStackSpotAgentID(string)              {}
func (f *fakeLLMManager) GetStackSpotRealm() string               { return "" }
func (f *fakeLLMManager) GetStackSpotAgentID() string             { return "" }
func (f *fakeLLMManager) RefreshProviders()                       {}
func (f *fakeLLMManager) CreateClientWithKey(_, _, _ string) (client.LLMClient, error) {
	return nil, errors.New("not implemented in test")
}
func (f *fakeLLMManager) CreateClientWithConfig(_, _, _ string, _ map[string]string) (client.LLMClient, error) {
	return nil, errors.New("not implemented in test")
}
func (f *fakeLLMManager) ListModelsForProvider(_ context.Context, _ string) ([]client.ModelInfo, error) {
	return nil, errors.New("not implemented in test")
}

// Compile-time check that fakeLLMManager satisfies the interface so
// the test breaks loudly if LLMManager grows new methods.
var _ manager.LLMManager = (*fakeLLMManager)(nil)

type fakeLLMClient struct{ model string }

func (f *fakeLLMClient) GetModelName() string { return f.model }
func (f *fakeLLMClient) SendPrompt(_ context.Context, _ string, _ []models.Message, _ int) (string, error) {
	return "", nil
}

// --- initFallbackChain ---------------------------------------------------

func TestInitFallbackChain_NoProvidersConfigured(t *testing.T) {
	opts := &ServerOptions{}
	llm := &fakeLLMManager{}
	sink := &fakeFallbackSink{}
	initFallbackChain(opts, llm, sink, zap.NewNop())
	if sink.chain != nil {
		t.Errorf("expected SetFallbackChain not called when FallbackProviders empty")
	}
	if llm.calls.Load() != 0 {
		t.Errorf("expected zero GetClient calls; got %d", llm.calls.Load())
	}
}

func TestInitFallbackChain_BelowTwoEntriesDoesNotWire(t *testing.T) {
	opts := &ServerOptions{
		FallbackProviders: "openai",
		Model:             "gpt-4",
	}
	llm := &fakeLLMManager{
		clients: map[string]client.LLMClient{"openai": &fakeLLMClient{model: "gpt-4"}},
	}
	sink := &fakeFallbackSink{}
	initFallbackChain(opts, llm, sink, zap.NewNop())
	if sink.chain != nil {
		t.Errorf("single provider should not wire a chain")
	}
}

func TestInitFallbackChain_SkipsUnavailableProviders(t *testing.T) {
	opts := &ServerOptions{
		FallbackProviders:  "openai,broken,claudeai",
		Model:              "gpt-4",
		FallbackMaxRetries: 2,
	}
	llm := &fakeLLMManager{
		clients: map[string]client.LLMClient{
			"openai":   &fakeLLMClient{model: "gpt-4"},
			"claudeai": &fakeLLMClient{model: "claude-3"},
		},
		errors: map[string]error{"broken": errors.New("no creds")},
	}
	sink := &fakeFallbackSink{}
	initFallbackChain(opts, llm, sink, zap.NewNop())
	if sink.chain == nil {
		t.Fatalf("expected chain wired with two healthy providers")
	}
}

func TestInitFallbackChain_PerProviderModelOverrideFromEnv(t *testing.T) {
	t.Setenv("CHATCLI_FALLBACK_MODEL_OPENAI", "gpt-4o")
	opts := &ServerOptions{
		FallbackProviders: "openai,claudeai",
		Model:             "default-model",
	}
	llm := &fakeLLMManager{
		clients: map[string]client.LLMClient{
			"openai":   &fakeLLMClient{},
			"claudeai": &fakeLLMClient{},
		},
	}
	sink := &fakeFallbackSink{}
	initFallbackChain(opts, llm, sink, zap.NewNop())
	if sink.chain == nil {
		t.Fatalf("expected chain wired")
	}
	// We can't introspect the entries through *fallback.Chain, but
	// the call counter ensures GetClient was invoked for both,
	// and the env-driven model branch is the only one that exercises
	// the strings.ToUpper path.
	if llm.calls.Load() != 2 {
		t.Errorf("expected 2 GetClient calls; got %d", llm.calls.Load())
	}
}

// --- initMCPManager ------------------------------------------------------

func TestInitMCPManager_NotConfiguredReturnsNil(t *testing.T) {
	t.Setenv("CHATCLI_MCP_ENABLED", "")
	opts := &ServerOptions{}
	sink := &fakeMCPSink{}
	stop := initMCPManager(opts, sink, zap.NewNop())
	if stop != nil {
		t.Errorf("expected nil stop closure when MCP not configured")
	}
	if sink.mgr != nil {
		t.Errorf("expected SetMCPManager not called")
	}
}

func TestInitMCPManager_MalformedConfigReturnsNil(t *testing.T) {
	// LoadConfig treats a missing file as "no config" (returns nil),
	// so the failure path we need to exercise is a syntactically
	// invalid JSON file — that surfaces an unmarshal error and the
	// helper must abort without wiring anything into the sink.
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "mcp_servers.json")
	if err := os.WriteFile(cfgPath, []byte(`{not valid json`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	opts := &ServerOptions{MCPConfigPath: cfgPath}
	sink := &fakeMCPSink{}
	stop := initMCPManager(opts, sink, zap.NewNop())
	if stop != nil {
		t.Errorf("expected nil stop closure when LoadConfig fails")
	}
	if sink.mgr != nil {
		t.Errorf("expected sink to remain unset on LoadConfig failure")
	}
}

func TestInitMCPManager_EmptyConfigSucceedsAndReturnsStopCloser(t *testing.T) {
	// Build an empty-but-valid mcp_servers.json so LoadConfig +
	// StartAll both succeed. With zero servers configured nothing
	// gets started, so the success path runs end-to-end without
	// spawning subprocesses.
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "mcp_servers.json")
	if err := os.WriteFile(cfgPath, []byte(`{"mcpServers":[]}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	opts := &ServerOptions{MCPConfigPath: cfgPath}
	sink := &fakeMCPSink{}
	stop := initMCPManager(opts, sink, zap.NewNop())
	if stop == nil {
		t.Fatalf("expected non-nil stop closure on success")
	}
	if sink.mgr == nil {
		t.Errorf("expected SetMCPManager wired on success")
	}
	// Stop closure must be safe to call and idempotent.
	stop()
}
