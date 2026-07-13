/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/diillson/chatcli/cli/mcp"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// TestRPCMCPToolAllowed_PolicyModes pins the exposure policy for proxied MCP
// tools: "all" admits everything, "safe" trusts only the origin server's
// readOnlyHint annotation, and the CSV allowlist matches the prefixed
// mcp_<tool> form.
func TestRPCMCPToolAllowed_PolicyModes(t *testing.T) {
	ro := mcp.MCPTool{Name: "list_regions", Annotations: map[string]interface{}{"readOnlyHint": true}}
	rw := mcp.MCPTool{Name: "run_script"}

	t.Setenv("CHATCLI_MCP_TOOLS", "all")
	mode, allow := rpcToolPolicy()
	if !rpcMCPToolAllowed(ro, mode, allow) || !rpcMCPToolAllowed(rw, mode, allow) {
		t.Error("all mode must admit every proxied MCP tool")
	}

	t.Setenv("CHATCLI_MCP_TOOLS", "safe")
	mode, allow = rpcToolPolicy()
	if !rpcMCPToolAllowed(ro, mode, allow) {
		t.Error("safe mode must admit readOnlyHint=true tools")
	}
	if rpcMCPToolAllowed(rw, mode, allow) {
		t.Error("safe mode must exclude tools without a read-only hint (fail closed)")
	}

	t.Setenv("CHATCLI_MCP_TOOLS", "read,mcp_run_script")
	mode, allow = rpcToolPolicy()
	if !rpcMCPToolAllowed(rw, mode, allow) {
		t.Error("allowlist must match the prefixed mcp_<tool> form")
	}
	if rpcMCPToolAllowed(ro, mode, allow) {
		t.Error("allowlist must exclude proxied tools it does not name")
	}
}

func TestMCPProxy_GuardsWithoutManager(t *testing.T) {
	c := &ChatCLI{} // MCP disabled
	if tools := c.ListMCPProxyTools(); tools != nil {
		t.Errorf("expected nil proxied tools without an MCP manager, got %v", tools)
	}
	if _, err := c.RunMCPProxyTool(context.Background(), "mcp_x", json.RawMessage(`{}`)); err == nil {
		t.Error("expected error when MCP is disabled")
	}
	select {
	case <-c.MCPStartupDone():
	default:
		t.Error("MCPStartupDone must be closed when MCP is disabled")
	}
	c.SetMCPToolsObserver(func() {}) // must not panic
}

// listingManager extends minimalManager with a configurable live-listing
// result per provider, mirroring what the interactive /model picker sees.
type listingManager struct {
	minimalManager
	models map[string][]client.ModelInfo
	errFor map[string]error
}

func (m *listingManager) ListModelsForProvider(_ context.Context, provider string) ([]client.ModelInfo, error) {
	if err, ok := m.errFor[provider]; ok {
		return nil, err
	}
	return m.models[provider], nil
}

// TestProvidersRPC_LiveModelsWithCatalogFallback pins the discovery surface:
// providers that answer the live listing surface those models (flagged
// api+catalog); providers that error fall back to the static catalog.
func TestProvidersRPC_LiveModelsWithCatalogFallback(t *testing.T) {
	mgr := &listingManager{
		minimalManager: minimalManager{providers: []string{"OPENAI", "CLAUDEAI"}},
		models: map[string][]client.ModelInfo{
			"OPENAI": {
				{ID: "gpt-live-1", Source: client.ModelSourceAPI},
				{ID: "gpt-catalogado", Source: client.ModelSourceCatalog},
			},
		},
		errFor: map[string]error{"CLAUDEAI": errors.New("api indisponível")},
	}
	c := &ChatCLI{logger: zap.NewNop(), manager: mgr, Provider: "OPENAI", Model: "gpt-live-1"}

	raw, err := c.ProvidersRPC()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		ActiveProvider string `json:"active_provider"`
		Providers      []struct {
			Name         string   `json:"name"`
			Active       bool     `json:"active"`
			Models       []string `json:"models"`
			ModelsSource string   `json:"models_source"`
		} `json:"providers"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, raw)
	}
	byName := map[string]int{}
	for i, p := range doc.Providers {
		byName[p.Name] = i
	}

	op := doc.Providers[byName["OPENAI"]]
	if op.ModelsSource != "api+catalog" {
		t.Errorf("OPENAI models_source = %q, want api+catalog", op.ModelsSource)
	}
	if len(op.Models) != 2 || op.Models[0] != "gpt-live-1" {
		t.Errorf("OPENAI must list the live models first: %v", op.Models)
	}
	if !op.Active {
		t.Error("OPENAI must be flagged active")
	}

	cl := doc.Providers[byName["CLAUDEAI"]]
	if cl.ModelsSource != "catalog" {
		t.Errorf("CLAUDEAI models_source = %q, want catalog fallback", cl.ModelsSource)
	}
	if len(cl.Models) == 0 {
		t.Error("CLAUDEAI fallback must still list catalog models")
	}
}

// TestSessionRPCWrappers_RoundTrip pins the store surface behind
// manage_session: save/list/load/delete against the real SessionManager,
// plus name validation on the remote-reachable paths and the disabled-store
// guards.
func TestSessionRPCWrappers_RoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sm, err := NewSessionManager(zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	c := &ChatCLI{sessionManager: sm}
	hist := []models.Message{{Role: "user", Content: "olá"}, {Role: "assistant", Content: "oi"}}

	if err := c.SaveSessionRPC("reuniao-mcp", hist); err != nil {
		t.Fatal(err)
	}
	names, err := c.ListSessionsRPC()
	if err != nil || len(names) != 1 || names[0] != "reuniao-mcp" {
		t.Fatalf("list after save wrong: %v, %v", names, err)
	}
	loaded, err := c.LoadSessionRPC("reuniao-mcp")
	if err != nil || len(loaded) != 2 || loaded[1].Content != "oi" {
		t.Fatalf("load wrong: %v, %v", loaded, err)
	}
	if err := c.DeleteSessionRPC("reuniao-mcp"); err != nil {
		t.Fatal(err)
	}
	if names, _ = c.ListSessionsRPC(); len(names) != 0 {
		t.Errorf("delete must empty the store, got %v", names)
	}

	// Remote-reachable name validation: traversal-ish names are rejected
	// before touching the filesystem.
	if _, err := c.LoadSessionRPC("../fora"); err == nil {
		t.Error("load must reject invalid names")
	}
	if err := c.DeleteSessionRPC("nome com espaço"); err == nil {
		t.Error("delete must reject invalid names")
	}
	if err := c.SaveSessionRPC("também/inválido", hist); err == nil {
		t.Error("save must reject invalid names")
	}

	// Store disabled (no session manager): every wrapper fails closed.
	off := &ChatCLI{}
	if err := off.SaveSessionRPC("x", hist); err == nil {
		t.Error("save without store must fail")
	}
	if _, err := off.LoadSessionRPC("x"); err == nil {
		t.Error("load without store must fail")
	}
	if _, err := off.ListSessionsRPC(); err == nil {
		t.Error("list without store must fail")
	}
	if err := off.DeleteSessionRPC("x"); err == nil {
		t.Error("delete without store must fail")
	}
}
