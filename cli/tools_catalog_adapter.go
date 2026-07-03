/*
 * ChatCLI - Adapter binding the @tools meta-tool to the plugin registry.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Implements plugins.ToolCatalogAdapter over the live plugin manager AND the
 * MCP manager: Describe renders one tool's full prompt block through the SAME
 * renderer the agent system prompt uses (an on-demand definition is
 * byte-identical to an inline one) — for mcp_* names it renders the tool's
 * JSON Schema from the connected server instead — and List renders the
 * one-line index of everything available, builtins and MCP alike.
 * Wired via plugins.SetToolCatalogAdapter at startup.
 */
package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/diillson/chatcli/cli/mcp"
	"github.com/diillson/chatcli/cli/plugins"
)

// toolCatalogPluginAdapter is the concrete plugins.ToolCatalogAdapter.
// mcpTools overrides the MCP snapshot source in tests; nil means the live
// manager (visibility-filtered by mcp.Manager.VisibleTools).
type toolCatalogPluginAdapter struct {
	cli      *ChatCLI
	mcpTools func() []mcp.MCPTool
}

// visibleMCPTools returns the MCP tools the LLM may see this turn. Empty when
// MCP is disabled, no server is connected, or every tool is masked.
func (a *toolCatalogPluginAdapter) visibleMCPTools() []mcp.MCPTool {
	if a.mcpTools != nil {
		return a.mcpTools()
	}
	if a.cli == nil || a.cli.mcpManager == nil {
		return nil
	}
	return a.cli.mcpManager.VisibleTools()
}

// Describe implements plugins.ToolCatalogAdapter.
func (a *toolCatalogPluginAdapter) Describe(name string) (string, error) {
	if a.cli.pluginManager == nil {
		return "", fmt.Errorf("plugin manager unavailable in this session")
	}
	want := strings.TrimSpace(name)
	want = strings.TrimPrefix(want, "@")

	// MCP names are routed by their canonical mcp_ prefix. Matching is
	// case-insensitive on our side; the stored name keeps the server's casing.
	mcpToolset := a.visibleMCPTools()
	if bare, ok := cutMCPPrefix(want); ok {
		if tool, ok := findMCPTool(mcpToolset, bare); ok {
			return renderMCPToolBlock(tool), nil
		}
	}

	ps := a.cli.pluginManager.GetPlugins()
	names := make([]string, 0, len(ps)+len(mcpToolset))
	for _, p := range ps {
		if strings.EqualFold(p.Name(), "@"+want) {
			return renderToolBlock(p, false), nil
		}
		names = append(names, p.Name())
	}

	// Bare-name fallback: models routinely drop the mcp_ prefix ("describe
	// read_file"). Only reached when no plugin matched, so a shadowing
	// builtin still wins.
	if tool, ok := findMCPTool(mcpToolset, want); ok {
		return renderMCPToolBlock(tool), nil
	}
	for _, t := range mcpToolset {
		names = append(names, "mcp_"+t.Name)
	}
	sort.Strings(names)
	return "", fmt.Errorf("unknown tool %q — available: %s", name, strings.Join(names, ", "))
}

// List implements plugins.ToolCatalogAdapter.
func (a *toolCatalogPluginAdapter) List() (string, error) {
	if a.cli.pluginManager == nil {
		return "", fmt.Errorf("plugin manager unavailable in this session")
	}
	ps := a.cli.pluginManager.GetPlugins()
	sort.Slice(ps, func(i, j int) bool { return ps[i].Name() < ps[j].Name() })
	mcpToolset := a.visibleMCPTools()
	var b strings.Builder
	fmt.Fprintf(&b, "%d tool(s) available (describe any of them for full usage):\n", len(ps)+len(mcpToolset))
	for _, p := range ps {
		b.WriteString(renderToolIndexLine(p))
	}
	if len(mcpToolset) > 0 {
		b.WriteString("MCP tools (external servers — describe before first use):\n")
		// Cluster by owning server so provenance reads at a glance; the
		// per-line [MCP:server] tag stays for models that quote lines out
		// of context. VisibleTools sorts by tool name, so within a server
		// the name order is preserved by the stable sort.
		sort.SliceStable(mcpToolset, func(i, j int) bool { return mcpToolset[i].ServerName < mcpToolset[j].ServerName })
		lastServer := ""
		for _, t := range mcpToolset {
			if t.ServerName != lastServer {
				fmt.Fprintf(&b, "  Servidor MCP %q:\n", t.ServerName)
				lastServer = t.ServerName
			}
			b.WriteString("  " + renderMCPToolIndexLine(t))
		}
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// cutMCPPrefix strips the canonical mcp_ prefix, reporting whether it was
// present. Case-insensitive so "MCP_Read_File" still routes to MCP.
func cutMCPPrefix(name string) (string, bool) {
	if len(name) >= 4 && strings.EqualFold(name[:4], "mcp_") {
		return name[4:], true
	}
	return "", false
}

// findMCPTool looks a tool up by its bare (unprefixed) name.
func findMCPTool(tools []mcp.MCPTool, bare string) (mcp.MCPTool, bool) {
	for _, t := range tools {
		if strings.EqualFold(t.Name, bare) {
			return t, true
		}
	}
	return mcp.MCPTool{}, false
}

// compile-time assertion that the adapter satisfies the plugin interface.
var _ plugins.ToolCatalogAdapter = (*toolCatalogPluginAdapter)(nil)
