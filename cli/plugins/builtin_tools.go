/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * @tools — the tool-catalog meta-tool behind the deferred catalog. The agent
 * system prompt inlines full definitions only for the core work loop; every
 * other builtin appears as a one-line index entry, and the model calls
 * @tools describe to pull a full definition (subcommands, flags, examples)
 * before first use. Same adapter seam as the other builtins.
 */
package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
)

// ToolCatalogAdapter is the surface @tools needs from the live session.
type ToolCatalogAdapter interface {
	// Describe returns the full prompt-grade definition of one tool.
	Describe(name string) (string, error)
	// List returns the one-line index of every available tool.
	List() (string, error)
}

type toolCatalogHolder struct{ a ToolCatalogAdapter }

var toolCatalogAtom atomic.Value // toolCatalogHolder

// SetToolCatalogAdapter wires the live adapter. Called from the top-level cli
// package at startup; pass nil to detach.
func SetToolCatalogAdapter(a ToolCatalogAdapter) {
	toolCatalogAtom.Store(toolCatalogHolder{a: a})
}

func currentToolCatalogAdapter() ToolCatalogAdapter {
	v := toolCatalogAtom.Load()
	if v == nil {
		return nil
	}
	h, _ := v.(toolCatalogHolder)
	return h.a
}

// BuiltinToolsPlugin is the @tools meta-tool.
type BuiltinToolsPlugin struct{}

// NewBuiltinToolsPlugin returns a ready-to-register plugin.
func NewBuiltinToolsPlugin() *BuiltinToolsPlugin { return &BuiltinToolsPlugin{} }

// Name returns "@tools".
func (*BuiltinToolsPlugin) Name() string { return "@tools" }

// Description surfaces the tool in /plugin list and the agent tool catalog.
func (*BuiltinToolsPlugin) Description() string {
	return "Tool catalog: get the full definition of any tool listed in the index before using it — builtins (subcommands, flags, examples) and MCP tools (mcp_* names, full parameter JSON Schema) — or list every available tool. Call describe once per unfamiliar tool instead of guessing its arguments."
}

// Usage explains the canonical invocation forms.
func (*BuiltinToolsPlugin) Usage() string {
	return `<tool_call name="@tools" args='{"cmd":"describe","args":{"name":"@diagram"}}' />
<tool_call name="@tools" args='{"cmd":"describe","args":{"name":"mcp_read_file"}}' />

Subcommands (cmd + args):
  describe {name}   full definition of one tool (builtin: subcommands/flags/examples; mcp_*: parameter JSON Schema)
  list     {}       one-line index of every available tool, MCP tools included`
}

// Version is semver; bumped when the surface changes.
func (*BuiltinToolsPlugin) Version() string { return "1.1.0" }

// Path is empty for builtin plugins.
func (*BuiltinToolsPlugin) Path() string { return "" }

// Schema exposes the structured description the agent prompt builder renders.
func (*BuiltinToolsPlugin) Schema() string {
	schema := map[string]interface{}{
		"argsFormat": "JSON envelope {cmd, args} preferred; argv form also accepted",
		"subcommands": []map[string]interface{}{
			{
				"name":        "describe",
				"description": "full definition of one tool: subcommands/flags/examples for builtins, parameter JSON Schema for MCP tools (mcp_* names)",
				"flags": []map[string]interface{}{
					{"name": "name", "description": "tool name, with or without the @ prefix; MCP tools use their mcp_ prefix", "type": "string", "required": true},
				},
				"examples": []string{`{"cmd":"describe","args":{"name":"@diagram"}}`, `{"cmd":"describe","args":{"name":"mcp_read_file"}}`},
			},
			{
				"name":        "list",
				"description": "one-line index of every available tool, including MCP tools from connected servers",
				"examples":    []string{`{"cmd":"list"}`},
			},
		},
	}
	b, _ := json.Marshal(schema)
	return string(b)
}

// Execute implements the plugin contract.
func (p *BuiltinToolsPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream mirrors Execute — no incremental output.
func (p *BuiltinToolsPlugin) ExecuteWithStream(_ context.Context, args []string, _ func(string)) (string, error) {
	adapter := currentToolCatalogAdapter()
	if adapter == nil {
		return "", errors.New("@tools: no tool-catalog adapter wired in this session")
	}
	if len(args) == 0 {
		return "", errors.New(`@tools: empty args. Example: <tool_call name="@tools" args='{"cmd":"describe","args":{"name":"@diagram"}}' />`)
	}

	cmd, inner, err := parseToolsInvocation(args)
	if err != nil {
		return "", fmt.Errorf("@tools: %w", err)
	}
	switch cmd {
	case "describe":
		var in struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal([]byte(inner), &in)
		if strings.TrimSpace(in.Name) == "" {
			return "", errors.New(`@tools describe: "name" is required`)
		}
		return adapter.Describe(in.Name)
	case "list":
		return adapter.List()
	default:
		return "", fmt.Errorf("@tools: unknown cmd %q (valid: describe|list)", cmd)
	}
}

// parseToolsInvocation accepts the JSON envelope {cmd, args} or the argv form
// ("describe @diagram" / "list").
func parseToolsInvocation(args []string) (string, string, error) {
	payload := strings.TrimSpace(strings.Join(args, " "))

	if strings.HasPrefix(payload, "{") {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(payload), &raw); err != nil {
			return "", "", fmt.Errorf(`parse envelope: %w. Expected {"cmd":"describe","args":{"name":"@x"}}`, err)
		}
		var cmdStr string
		if rc, ok := raw["cmd"]; ok {
			_ = json.Unmarshal(rc, &cmdStr)
		}
		canon := canonicalToolsCmd(cmdStr)
		if canon == "" {
			return "", "", fmt.Errorf("missing or unknown cmd %q (valid: describe|list)", cmdStr)
		}
		var inner string
		if rargs, ok := raw["args"]; ok && len(rargs) > 0 {
			inner = string(rargs)
		} else {
			delete(raw, "cmd")
			b, _ := json.Marshal(raw)
			inner = string(b)
		}
		return canon, inner, nil
	}

	canon := canonicalToolsCmd(args[0])
	if canon == "" {
		return "", "", fmt.Errorf("unknown cmd %q (valid: describe|list)", args[0])
	}
	in := map[string]interface{}{}
	if len(args) > 1 {
		in["name"] = args[1]
	}
	b, _ := json.Marshal(in)
	return canon, string(b), nil
}

// canonicalToolsCmd folds aliases onto the canonical subcommand names.
func canonicalToolsCmd(cmd string) string {
	switch strings.ToLower(strings.TrimSpace(cmd)) {
	case "describe", "desc", "show", "help", "schema":
		return "describe"
	case "list", "ls", "index", "catalog":
		return "list"
	default:
		return ""
	}
}
