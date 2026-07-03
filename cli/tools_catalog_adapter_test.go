/*
 * ChatCLI - tests for the @tools catalog adapter and /config agent catalog.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/mcp"
	"github.com/diillson/chatcli/cli/plugins"
	"go.uber.org/zap"
)

func newCatalogCLI(t *testing.T) *ChatCLI {
	t.Helper()
	mgr, err := plugins.NewManager(zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	mgr.RegisterBuiltinPlugin(plugins.NewBuiltinDiagramPlugin())
	mgr.RegisterBuiltinPlugin(plugins.NewBuiltinToolsPlugin())
	return &ChatCLI{pluginManager: mgr, logger: zap.NewNop()}
}

// TestToolCatalogAdapterDescribe pins the meta-tool's core promise: the
// on-demand definition is the SAME full block the inline prompt would carry.
func TestToolCatalogAdapterDescribe(t *testing.T) {
	cli := newCatalogCLI(t)
	a := &toolCatalogPluginAdapter{cli: cli}

	out, err := a.Describe("@diagram")
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if !strings.Contains(out, "- Ferramenta: @diagram") || !strings.Contains(out, "Subcomandos Disponíveis:") {
		t.Fatalf("Describe must return the full prompt block, got: %.120s", out)
	}

	// Name without the @ prefix resolves too (models drop it routinely).
	if _, err := a.Describe("diagram"); err != nil {
		t.Fatalf("prefixless describe: %v", err)
	}

	// Unknown tool errors and lists what exists.
	if _, err := a.Describe("@nope"); err == nil || !strings.Contains(err.Error(), "@diagram") {
		t.Fatalf("unknown tool must list available names, got %v", err)
	}
}

// TestToolCatalogAdapterList pins the index rendering.
func TestToolCatalogAdapterList(t *testing.T) {
	cli := newCatalogCLI(t)
	a := &toolCatalogPluginAdapter{cli: cli}

	out, err := a.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !strings.Contains(out, "- @diagram: ") || !strings.Contains(out, "- @tools: ") {
		t.Fatalf("List missing index lines: %q", out)
	}
	if strings.Contains(out, "Subcomandos") {
		t.Fatal("List must stay one line per tool — no schemas")
	}
}

// fakeMCPTools is the test seam for the adapter's MCP snapshot source —
// the same visibility-filtered shape mcp.Manager.VisibleTools returns.
func fakeMCPTools() []mcp.MCPTool {
	return []mcp.MCPTool{
		{
			Name:        "read_file",
			Description: "Reads a file from the sandbox. Paths are server-relative.",
			ServerName:  "fs",
			Parameters: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{"path": map[string]interface{}{"type": "string"}},
				"required":   []interface{}{"path"},
			},
		},
		{Name: "ping", Description: "Health check", ServerName: "fs"},
		{Name: "create_issue", Description: "Opens a GitHub issue", ServerName: "gh"},
	}
}

// TestToolCatalogAdapterDescribeMCP pins the fix for the MCP blind spot: the
// meta-tool must return an MCP tool's full parameter schema instead of
// "unknown tool", so the model never has to invoke blind to discover args.
func TestToolCatalogAdapterDescribeMCP(t *testing.T) {
	cli := newCatalogCLI(t)
	a := &toolCatalogPluginAdapter{cli: cli, mcpTools: fakeMCPTools}

	out, err := a.Describe("mcp_read_file")
	if err != nil {
		t.Fatalf("Describe mcp tool: %v", err)
	}
	for _, want := range []string{
		"- Ferramenta: mcp_read_file (MCP, servidor: fs)",
		"Reads a file from the sandbox.",
		`"required"`,
		`"path"`,
		`<tool_call name="mcp_read_file"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Describe(mcp_read_file) missing %q in:\n%s", want, out)
		}
	}

	// Models drop prefixes routinely: "@mcp_x" and the bare server-side name
	// both resolve to the same block.
	for _, alias := range []string{"@mcp_read_file", "read_file", "MCP_READ_FILE"} {
		got, err := a.Describe(alias)
		if err != nil {
			t.Fatalf("Describe(%q): %v", alias, err)
		}
		if got != out {
			t.Errorf("Describe(%q) diverged from canonical block", alias)
		}
	}

	// A tool that legitimately takes no params must say so explicitly.
	pingOut, err := a.Describe("mcp_ping")
	if err != nil {
		t.Fatalf("Describe mcp_ping: %v", err)
	}
	if !strings.Contains(pingOut, "args='{}'") {
		t.Errorf("schemaless tool must instruct empty-args invocation, got:\n%s", pingOut)
	}

	// Unknown names list MCP tools alongside builtins.
	if _, err := a.Describe("@nope"); err == nil || !strings.Contains(err.Error(), "mcp_read_file") {
		t.Fatalf("unknown-tool error must list MCP names, got %v", err)
	}

	// No MCP wired (chat sessions, MCP disabled): plugin behavior intact,
	// mcp_ lookups fail cleanly instead of panicking.
	plain := &toolCatalogPluginAdapter{cli: cli}
	if _, err := plain.Describe("@diagram"); err != nil {
		t.Fatalf("plugin describe without MCP: %v", err)
	}
	if _, err := plain.Describe("mcp_read_file"); err == nil {
		t.Fatal("mcp describe without MCP manager must error, not panic")
	}
}

// TestToolCatalogAdapterListMCP pins the index: MCP tools appear as one-line
// entries under their own section, builtin count + MCP count aggregated.
func TestToolCatalogAdapterListMCP(t *testing.T) {
	cli := newCatalogCLI(t)
	a := &toolCatalogPluginAdapter{cli: cli, mcpTools: fakeMCPTools}

	out, err := a.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !strings.Contains(out, "5 tool(s) available") {
		t.Errorf("List must count builtins+MCP (2+3), got: %q", out)
	}
	if !strings.Contains(out, "- mcp_read_file [MCP:fs]: Reads a file from the sandbox.") {
		t.Errorf("List missing MCP index line: %q", out)
	}
	if !strings.Contains(out, "- mcp_ping [MCP:fs]: Health check") {
		t.Errorf("List missing schemaless MCP index line: %q", out)
	}
	if strings.Contains(out, `"properties"`) {
		t.Error("List must stay one line per tool — no schemas")
	}
	// Tools cluster under one header per owning server, servers in order.
	fsHdr := strings.Index(out, `Servidor MCP "fs":`)
	ghHdr := strings.Index(out, `Servidor MCP "gh":`)
	if fsHdr < 0 || ghHdr < 0 || fsHdr > ghHdr {
		t.Errorf("List must group MCP tools under per-server headers in order, got: %q", out)
	}
	if issue := strings.Index(out, "- mcp_create_issue [MCP:gh]:"); issue < ghHdr {
		t.Errorf("gh tool must sit under the gh server header, got: %q", out)
	}

	// Without MCP the section is absent entirely.
	plain := &toolCatalogPluginAdapter{cli: cli}
	plainOut, err := plain.List()
	if err != nil {
		t.Fatalf("List without MCP: %v", err)
	}
	if strings.Contains(plainOut, "MCP tools (external servers") {
		t.Errorf("no-MCP list must not render an MCP section: %q", plainOut)
	}
}

// TestConfigAgentCatalogSwitch pins the runtime flip: the mutator mirrors the
// env var, and toolCatalogDeferred reflects it immediately.
func TestConfigAgentCatalogSwitch(t *testing.T) {
	prev, had := os.LookupEnv(toolCatalogEnvVar)
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(toolCatalogEnvVar, prev)
		} else {
			_ = os.Unsetenv(toolCatalogEnvVar)
		}
	})

	cli := newCatalogCLI(t)
	cli.configAgentCatalog([]string{"full"})
	if toolCatalogDeferred() {
		t.Fatal("catalog full did not flip the runtime mode")
	}
	cli.configAgentCatalog([]string{"deferred"})
	if !toolCatalogDeferred() {
		t.Fatal("catalog deferred did not flip back")
	}
	// Invalid value: mode unchanged.
	cli.configAgentCatalog([]string{"banana"})
	if !toolCatalogDeferred() {
		t.Fatal("invalid value must not change the mode")
	}
	// No-arg show path must not panic.
	cli.configAgentCatalog(nil)
}
