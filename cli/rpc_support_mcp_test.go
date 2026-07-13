/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/diillson/chatcli/cli/mcp"
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
