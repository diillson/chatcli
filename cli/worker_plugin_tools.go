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
	"github.com/diillson/chatcli/pkg/textfold"
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
	// The worker's tool dispatch already reports this call on the live
	// telemetry bus; the uninstrumented runner avoids counting it twice.
	return runBuiltin(ctx, plugin, argv)
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
// Schema(). A tool with one subcommand (or none) takes its arguments flat,
// typed from its flags; a tool with several keeps the {cmd, args} envelope
// every builtin's parser accepts, with cmd an enum of the subcommands and
// args typed as the union of their flags, each flag saying which
// subcommands use it — so the model sees the arguments, not just the verbs.
func synthesizePluginDef(defName string, plugin plugins.Plugin) (models.ToolDefinition, bool) {
	var schema struct {
		Description string                 `json:"description"`
		ArgsFormat  string                 `json:"argsFormat"`
		Parameters  map[string]interface{} `json:"parameters"`
		Flags       []nativeSchemaFlag     `json:"flags"`
		Subcommands []struct {
			Name        string             `json:"name"`
			Description string             `json:"description"`
			Flags       []nativeSchemaFlag `json:"flags"`
		} `json:"subcommands"`
	}
	if json.Unmarshal([]byte(plugin.Schema()), &schema) != nil {
		return models.ToolDefinition{}, false
	}
	// A tool without subcommands takes its arguments flat: hand the model
	// those arguments (the JSON Schema the tool ships, or one built from its
	// flags) instead of a cmd/args envelope with nothing inside.
	if len(schema.Subcommands) == 0 {
		params := schema.Parameters
		if params == nil {
			params = flagsJSONSchema(schema.Flags)
		}
		if params != nil {
			return models.ToolDefinition{
				Type:     "function",
				Function: models.ToolFunctionDef{Name: defName, Description: plugin.Description(), Parameters: params},
			}, true
		}
	}
	// One subcommand is the tool itself (read, search, tree…): flat, typed.
	if len(schema.Subcommands) == 1 {
		if params := flagsJSONSchema(schema.Subcommands[0].Flags); params != nil {
			desc := strings.TrimSpace(schema.Subcommands[0].Description)
			if desc == "" {
				desc = plugin.Description()
			}
			return models.ToolDefinition{
				Type:     "function",
				Function: models.ToolFunctionDef{Name: defName, Description: desc, Parameters: params},
			}, true
		}
	}
	cmds := make([]string, 0, len(schema.Subcommands))
	var b strings.Builder
	b.WriteString(schema.Description)
	if len(schema.Subcommands) > 0 {
		b.WriteString(" Subcommands: ")
	}
	// args is the union of every subcommand's flags, each saying who uses it.
	argProps := map[string]interface{}{}
	usedBy := map[string][]string{}
	for i, sc := range schema.Subcommands {
		cmds = append(cmds, sc.Name)
		if i > 0 {
			b.WriteString("; ")
		}
		fmt.Fprintf(&b, "%s (%s)", sc.Name, sc.Description)
		if names := flagNames(sc.Flags); names != "" {
			fmt.Fprintf(&b, " args: %s", names)
		}
		for _, f := range sc.Flags {
			if f.Name == "" {
				continue
			}
			usedBy[f.Name] = append(usedBy[f.Name], sc.Name)
			if _, seen := argProps[f.Name]; !seen {
				argProps[f.Name] = flagJSONSchemaProp(f)
			}
		}
	}
	for name, who := range usedBy {
		if prop, ok := argProps[name].(map[string]interface{}); ok && len(who) < len(cmds) {
			prop["description"] = strings.TrimSpace(fmt.Sprintf("%v (%s)", prop["description"], strings.Join(who, ", ")))
		}
	}
	desc := plugin.Description()
	if d := strings.TrimSpace(b.String()); d != "" {
		desc = d
	}
	desc = textfold.Cut(desc, nativeDefDescriptionMax)
	cmdProp := map[string]interface{}{"type": "string", "description": "subcommand"}
	if len(cmds) > 0 {
		cmdProp["enum"] = cmds
	}
	argsProp := map[string]interface{}{"type": "object", "description": "subcommand arguments"}
	if len(argProps) > 0 {
		argsProp["properties"] = argProps
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
					"args": argsProp,
				},
				"required": []string{"cmd"},
			},
		},
	}, true
}

// nativeSchemaFlag is one flag of a builtin's Schema(), tolerant of the
// default being a number or a boolean rather than a string.
type nativeSchemaFlag struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Type        string      `json:"type"`
	Default     interface{} `json:"default"`
	Required    bool        `json:"required"`
}

// flagNames renders "a, b?, c" (optional flags marked with ?).
func flagNames(flags []nativeSchemaFlag) string {
	parts := make([]string, 0, len(flags))
	for _, f := range flags {
		if f.Name == "" {
			continue
		}
		if f.Required {
			parts = append(parts, f.Name)
		} else {
			parts = append(parts, f.Name+"?")
		}
	}
	return strings.Join(parts, ", ")
}

// flagJSONSchemaProp is one flag as a JSON Schema property.
func flagJSONSchemaProp(f nativeSchemaFlag) map[string]interface{} {
	typ := strings.ToLower(strings.TrimSpace(f.Type))
	switch typ {
	case "string", "integer", "number", "boolean", "array", "object":
	case "int":
		typ = "integer"
	case "bool":
		typ = "boolean"
	default:
		typ = "string"
	}
	prop := map[string]interface{}{"type": typ, "description": textfold.Cut(f.Description, nativeDefFlagDescriptionMax)}
	if typ == "array" {
		prop["items"] = map[string]interface{}{}
	}
	return prop
}

// nativeDefDescriptionMax and nativeDefFlagDescriptionMax bound a
// synthesized definition: every builtin's schema travels with the turn,
// so a verbose one would crowd the others out of the defer threshold.
const (
	nativeDefDescriptionMax     = 700
	nativeDefFlagDescriptionMax = 160
)

// flagsJSONSchema turns a builtin's flags into the JSON Schema of a
// native tool's parameters, nil when there are none.
func flagsJSONSchema(flags []nativeSchemaFlag) map[string]interface{} {
	if len(flags) == 0 {
		return nil
	}
	props := make(map[string]interface{}, len(flags))
	var required []string
	for _, f := range flags {
		if f.Name == "" {
			continue
		}
		props[f.Name] = flagJSONSchemaProp(f)
		if f.Required {
			required = append(required, f.Name)
		}
	}
	if len(props) == 0 {
		return nil
	}
	out := map[string]interface{}{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}
