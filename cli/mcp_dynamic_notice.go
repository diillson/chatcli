/*
 * ChatCLI - MCP Dynamic Tool Notice
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Renders the mid-loop notice the agent injects when a server's dynamic
 * tool list changed (notifications/tools/list_changed → registry refresh
 * in cli/mcp/dynamic_tools.go). Without this the model keeps reasoning
 * against the catalog it saw in the system prompt and never discovers the
 * tools that appeared after a bootstrap call (e.g. HTTP Toolkit's start).
 */
package cli

import (
	"fmt"
	"strings"

	"github.com/diillson/chatcli/cli/mcp"
)

// mcpNoticeMaxNames caps how many tool names are listed per change. The
// full, always-current catalog is one @tools list away — the notice only
// needs to make the model aware the catalog moved.
const mcpNoticeMaxNames = 20

// buildMCPToolChangeNotice renders the model-facing notice for a batch of
// reconciled tool-list refreshes. English on purpose — prompt text, like
// every other injected block. Returns "" for an empty batch.
func buildMCPToolChangeNotice(changes []mcp.ToolListChange) string {
	if len(changes) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("[MCP TOOL CATALOG UPDATED] Dynamic MCP servers changed their tool list mid-session:\n")
	for _, c := range changes {
		fmt.Fprintf(&b, "- Server %q:", c.Server)
		if len(c.Added) > 0 {
			fmt.Fprintf(&b, " +%d new tool(s): %s.", len(c.Added), joinPrefixed(c.Added, mcpNoticeMaxNames))
		}
		if len(c.Removed) > 0 {
			fmt.Fprintf(&b, " %d removed (do NOT call these anymore): %s.", len(c.Removed), joinPrefixed(c.Removed, mcpNoticeMaxNames))
		}
		b.WriteString("\n")
	}
	b.WriteString("New tools are invocable as <tool_call name=\"mcp_<tool>\" args='{...}' />. " +
		"Get the parameter schema with @tools describe before first use; " +
		"@tools list shows the full current catalog.")
	return b.String()
}

// joinPrefixed renders tool names with the mcp_ invocation prefix, capped
// at maxNames with an overflow suffix.
func joinPrefixed(names []string, maxNames int) string {
	shown := names
	overflow := 0
	if len(shown) > maxNames {
		overflow = len(shown) - maxNames
		shown = shown[:maxNames]
	}
	prefixed := make([]string, len(shown))
	for i, n := range shown {
		prefixed[i] = "mcp_" + n
	}
	out := strings.Join(prefixed, ", ")
	if overflow > 0 {
		out += fmt.Sprintf(" (+%d more)", overflow)
	}
	return out
}

// summarizeMCPToolChanges renders the compact user-facing summary shown in
// the terminal, e.g. "http-toolkit: +12/-1".
func summarizeMCPToolChanges(changes []mcp.ToolListChange) string {
	parts := make([]string, 0, len(changes))
	for _, c := range changes {
		parts = append(parts, fmt.Sprintf("%s: +%d/-%d", c.Server, len(c.Added), len(c.Removed)))
	}
	return strings.Join(parts, ", ")
}
