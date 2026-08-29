/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * cli-side wiring for the workers' per-task plugin grants (see
 * cli/agent/workers/plugin_tools.go). The workers package cannot reach the
 * plugin or MCP managers, so this file provides:
 *   - runWorkerPluginTool: execute one granted call (builtin or mcp_*),
 *   - workerPluginToolDefs: translate grant names into native tool defs.
 *
 * Concurrency: @browser (and any plugin declaring IsConcurrencySafe=false)
 * drives one process-wide stateful session; parallel workers would corrupt
 * it. We serialize non-concurrency-safe plugins with a per-plugin mutex —
 * the first such enforcement in the process. This prevents interleaved CDP
 * ops; it does NOT prevent one worker's snapshot refs being invalidated by
 * another worker's navigation, so the skill advises granting @browser to a
 * single task.
 */
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/diillson/chatcli/cli/agent/workers"
	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/models"
)

var (
	workerPluginLocksMu sync.Mutex
	workerPluginLocks   = map[string]*sync.Mutex{}
)

// workerPluginLock returns the process-wide serialization mutex for a plugin.
func workerPluginLock(name string) *sync.Mutex {
	workerPluginLocksMu.Lock()
	defer workerPluginLocksMu.Unlock()
	mu, ok := workerPluginLocks[name]
	if !ok {
		mu = &sync.Mutex{}
		workerPluginLocks[name] = mu
	}
	return mu
}

// runWorkerPluginTool executes one granted plugin call. tool is the canonical
// grant name ("@browser" | "mcp_x"); argsJSON is the {cmd,args} envelope.
// The worker already applied the security policy — this must NOT re-check.
func (cli *ChatCLI) runWorkerPluginTool(ctx context.Context, tool, argsJSON string) (string, error) {
	tool = strings.TrimSpace(tool)
	if strings.HasPrefix(tool, "mcp_") {
		if cli.mcpManager == nil {
			return "", fmt.Errorf("%s: MCP not available in this session", tool)
		}
		return cli.RunMCPProxyTool(ctx, tool, json.RawMessage(argsJSON))
	}
	name := tool
	if !strings.HasPrefix(name, "@") {
		name = "@" + name
	}
	plugin, ok := cli.pluginManager.GetPlugin(name)
	if !ok {
		return "", fmt.Errorf("%s: plugin not found", name)
	}
	argv := []string{argsJSON}
	if !plugins.IsConcurrencySafe(plugin, argv) {
		mu := workerPluginLock(name)
		mu.Lock()
		defer mu.Unlock()
	}
	return execBuiltin(ctx, plugin, argv)
}

// workerPluginToolDefs translates grant names into native tool definitions:
// mcp_* filtered from the MCP catalog (origin schema verbatim), the six
// natively-mapped builtins from the worker catalog, and every other builtin
// synthesized from its Schema().
func (cli *ChatCLI) workerPluginToolDefs(names []string) []models.ToolDefinition {
	mapped := workers.PluginToolDefinitions()
	mappedByName := make(map[string]models.ToolDefinition, len(mapped))
	for _, d := range mapped {
		mappedByName[d.Function.Name] = d
	}
	var mcpDefs []models.ToolDefinition
	if cli.mcpManager != nil {
		mcpDefs = cli.mcpManager.GetTools()
	}
	mcpByName := make(map[string]models.ToolDefinition, len(mcpDefs))
	for _, d := range mcpDefs {
		mcpByName[d.Function.Name] = d
	}

	out := make([]models.ToolDefinition, 0, len(names))
	for _, grant := range names {
		if strings.HasPrefix(grant, "mcp_") {
			if d, ok := mcpByName[grant]; ok {
				out = append(out, d)
			}
			continue
		}
		defName := strings.TrimPrefix(grant, "@")
		// A builtin already exposed as a native tool keeps that mapping so
		// its args schema matches what ResolveNativePluginTool expects.
		if d, ok := mappedByName[nativeNameForBuiltin(grant)]; ok {
			renamed := d
			renamed.Function.Name = defName
			out = append(out, renamed)
			continue
		}
		if plugin, ok := cli.pluginManager.GetPlugin(grant); ok {
			if d, ok := synthesizePluginDef(defName, plugin); ok {
				out = append(out, d)
			}
		}
	}
	return out
}

// nativeNameForBuiltin maps a grant to the native function name the six
// mapped builtins use (e.g. "@websearch" → "web_search"), or "".
func nativeNameForBuiltin(grant string) string {
	switch grant {
	case "@websearch":
		return "web_search"
	case "@webfetch":
		return "web_fetch"
	case "@mail":
		return "squad_mail"
	case "@agents":
		return "agents_runs"
	case "@board":
		return "board_cards"
	}
	return ""
}

// synthesizePluginDef builds a native tool definition from a builtin's
// Schema() — a {cmd: enum(subcommands), args: object} envelope that matches
// every builtin's own lenient parser.
func synthesizePluginDef(defName string, plugin plugins.Plugin) (models.ToolDefinition, bool) {
	var schema struct {
		Description string `json:"description"`
		ArgsFormat  string `json:"argsFormat"`
		Subcommands []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"subcommands"`
	}
	if json.Unmarshal([]byte(plugin.Schema()), &schema) != nil {
		return models.ToolDefinition{}, false
	}
	cmds := make([]string, 0, len(schema.Subcommands))
	var b strings.Builder
	b.WriteString(schema.Description)
	if len(schema.Subcommands) > 0 {
		b.WriteString(" Subcommands: ")
	}
	for i, sc := range schema.Subcommands {
		cmds = append(cmds, sc.Name)
		if i > 0 {
			b.WriteString("; ")
		}
		fmt.Fprintf(&b, "%s (%s)", sc.Name, sc.Description)
	}
	desc := plugin.Description()
	if d := strings.TrimSpace(b.String()); d != "" {
		desc = d
	}
	cmdProp := map[string]interface{}{"type": "string", "description": "subcommand"}
	if len(cmds) > 0 {
		cmdProp["enum"] = cmds
	}
	return models.ToolDefinition{
		Type: "function",
		Function: models.ToolFunctionDef{
			Name:        defName,
			Description: desc,
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"cmd":  cmdProp,
					"args": map[string]interface{}{"type": "object", "description": "subcommand arguments"},
				},
				"required": []string{"cmd"},
			},
		},
	}, true
}
