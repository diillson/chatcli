/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/cli/agent/workers"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// Every builtin as a native tool.
//
// Six builtins had hand-written native definitions; the other forty-odd
// reached a model with native function calling only through the XML
// <tool_call> fallback described in the text catalog — two protocols in
// one turn, and the structured one covered the minority. The definitions
// below are synthesized from each builtin's own Schema(), the same way a
// worker's granted tools are, and travel with the MCP tools: under the
// defer threshold they all ship, past it they are reachable through
// find_tools. A native call that names a builtin is routed to it with
// the {cmd,args} envelope every builtin's parser accepts.

// builtinNativeDefs returns the native definitions of every builtin that
// is not already mapped by hand, not reserved for the loop itself, and
// allowed by the slash command's tool scope, sorted by name.
func (a *AgentMode) builtinNativeDefs() []models.ToolDefinition {
	if a == nil || a.cli == nil || a.cli.pluginManager == nil {
		return nil
	}
	var names []string
	for _, p := range a.cli.pluginManager.GetPlugins() {
		name := strings.ToLower(strings.TrimSpace(p.Name()))
		if !strings.HasPrefix(name, "@") || !nativeBuiltinEligible(name) || !a.commandScopeAllows(name) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	defs := a.cli.workerPluginToolDefs(names)
	out := make([]models.ToolDefinition, 0, len(defs))
	for _, d := range defs {
		if _, coder := workers.NativeToolNameToSubcmd(d.Function.Name); coder || d.Function.Name == workers.FindToolsName {
			// A builtin whose bare name is also a coder native tool would be
			// ambiguous on the way back; it keeps the XML path.
			if a.logger != nil {
				a.logger.Warn("builtin skipped as native tool: name collides with a coder tool", zap.String("tool", d.Function.Name))
			}
			continue
		}
		out = append(out, d)
	}
	return out
}

// nativeBuiltinEligible says whether a builtin gets a synthesized native
// definition: @coder has its own, the hand-mapped six keep theirs, and the
// tools the loop handles specially (@park suspends the turn, @ask opens an
// overlay, @view attaches images, @taskgraph streams a run, @model
// re-routes) stay on the path that knows them.
func nativeBuiltinEligible(name string) bool {
	if name == "@coder" || workers.IsNativeBuiltin(name) {
		return false
	}
	return !workers.PluginGrantDenied(name)
}

// nativeBuiltinCall maps a native call that names a builtin to the
// builtin's envelope; false when the name is not a builtin's.
func (a *AgentMode) nativeBuiltinCall(ntc models.ToolCall) (agent.ToolCall, bool) {
	if a == nil || a.cli == nil || a.cli.pluginManager == nil {
		return agent.ToolCall{}, false
	}
	name := "@" + strings.TrimPrefix(strings.ToLower(strings.TrimSpace(ntc.Name)), "@")
	if !nativeBuiltinEligible(name) {
		return agent.ToolCall{}, false
	}
	if _, coder := workers.NativeToolNameToSubcmd(ntc.Name); coder {
		return agent.ToolCall{}, false
	}
	if _, ok := a.cli.pluginManager.GetPlugin(name); !ok {
		return agent.ToolCall{}, false
	}
	args := ntc.Arguments
	if args == nil {
		args = map[string]interface{}{}
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return agent.ToolCall{}, false
	}
	return agent.ToolCall{Name: name, Args: string(raw), Raw: string(raw)}, true
}
