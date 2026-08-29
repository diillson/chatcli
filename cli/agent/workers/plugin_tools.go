/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * Per-task plugin tool grants for squad workers.
 *
 * A worker's tool surface is deliberately narrow (engine subcommands +
 * read-only context tools). This file adds the OPT-IN escape hatch: a
 * dispatch call may grant specific session plugins (@browser, @websearch,
 * mcp_* …) to the worker it spawns. The workers package never touches the
 * plugin or MCP managers — the cli package registers a definer (grant names
 * → native tool definitions) and a runner (one call → output), mirroring
 * the context-tools seam. Definitions without a registered runner are never
 * emitted: a definition without an executor would guarantee a failing call.
 *
 * Grants ride the dispatch context (CtxKeyPluginGrant) so every agent type
 * and the quality pipeline inherit them with zero signature changes, and
 * AgentCall carries them via a POINTER field — the struct must remain
 * comparable (Floor 13 treats losing comparability as a breaking change).
 */
package workers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/diillson/chatcli/models"
)

// PluginGrant names the session plugins a dispatched worker may call.
// Builtin names are canonical with their "@" prefix ("@browser"); MCP tools
// keep their proxy name ("mcp_search_docs").
type PluginGrant struct {
	Plugins []string
}

// CtxKeyPluginGrant carries a *PluginGrant through the dispatch context.
const CtxKeyPluginGrant ctxKey = "plugin_grant"

// pluginGrantDenylist lists tools that must never be granted to a worker:
// they are intercepted by the orchestrator loop or own session-level state
// (interactive overlays, vision staging, the task-graph engine itself,
// session model routing, park/resume semantics).
var pluginGrantDenylist = map[string]bool{
	"@ask": true, "@view": true, "@taskgraph": true, "@model": true, "@park": true,
}

// Model-facing prompt strings, named per house style (never inline literals).
const (
	grantedPluginsPromptHeader = "## GRANTED SESSION TOOLS\nThis task additionally grants you: "
	grantedPluginsPromptUsage  = ".\nInvoke them as native tools with {\"cmd\": <subcommand>, \"args\": {...}}; every call passes the session security policy."
)

var (
	// pluginToolRunner executes one granted plugin call. tool is the
	// canonical grant name; argsJSON is the {"cmd":…,"args":{…}} envelope.
	pluginToolRunner func(ctx context.Context, tool string, argsJSON string) (string, error)

	// pluginToolDefiner maps grant names to native tool definitions.
	pluginToolDefiner func(names []string) []models.ToolDefinition
)

// RegisterPluginToolRunner wires (or clears, with nil) the executor for
// granted plugin tools.
func RegisterPluginToolRunner(fn func(ctx context.Context, tool string, argsJSON string) (string, error)) {
	workerHooksMu.Lock()
	pluginToolRunner = fn
	workerHooksMu.Unlock()
}

// RegisterPluginToolDefiner wires (or clears, with nil) the translator from
// grant names to native tool definitions.
func RegisterPluginToolDefiner(fn func(names []string) []models.ToolDefinition) {
	workerHooksMu.Lock()
	pluginToolDefiner = fn
	workerHooksMu.Unlock()
}

func currentPluginToolRunner() func(context.Context, string, string) (string, error) {
	workerHooksMu.RLock()
	defer workerHooksMu.RUnlock()
	return pluginToolRunner
}

func currentPluginToolDefiner() func([]string) []models.ToolDefinition {
	workerHooksMu.RLock()
	defer workerHooksMu.RUnlock()
	return pluginToolDefiner
}

// NormalizePluginGrant canonicalizes and filters grant names: lowercase,
// "@" prefix restored for builtins, denylisted and empty entries dropped,
// duplicates removed. Order is preserved.
func NormalizePluginGrant(names []string) []string {
	seen := make(map[string]bool, len(names))
	out := make([]string, 0, len(names))
	for _, raw := range names {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" {
			continue
		}
		if !strings.HasPrefix(name, "@") && !strings.HasPrefix(name, "mcp_") {
			name = "@" + name
		}
		if pluginGrantDenylist[name] || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// grantFromContext returns the normalized grant carried by the dispatch
// context, or nil.
func grantFromContext(ctx context.Context) []string {
	g, _ := ctx.Value(CtxKeyPluginGrant).(*PluginGrant)
	if g == nil {
		return nil
	}
	return NormalizePluginGrant(g.Plugins)
}

// pluginDefName is the native function name a grant is exposed under:
// builtins drop the "@" ("browser"); MCP proxies keep their name verbatim.
func pluginDefName(grant string) string {
	return strings.TrimPrefix(grant, "@")
}

// pluginGrantForName maps a resolved call name back to its canonical grant
// ("browser" or "@browser" → "@browser"; "mcp_x" → "mcp_x").
func pluginGrantForName(pluginTools []string, name string) (string, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, grant := range pluginTools {
		if name == grant || name == pluginDefName(grant) {
			return grant, true
		}
	}
	return "", false
}

// PluginToolDefinitionsFor returns the native definitions for the granted
// tools, nil when no runner or definer is registered.
func PluginToolDefinitionsFor(names []string) []models.ToolDefinition {
	if len(names) == 0 || currentPluginToolRunner() == nil {
		return nil
	}
	definer := currentPluginToolDefiner()
	if definer == nil {
		return nil
	}
	return definer(names)
}

// pluginEnvelopeJSON renders the {"cmd":…,"args":{…}} envelope for a
// resolved plugin call — the shape every builtin's own parser accepts, and
// the policy surface NormalizeCoderArgs understands.
func pluginEnvelopeJSON(rtc resolvedToolCall) string {
	if rtc.Native {
		if len(rtc.NativeArgs) > 0 {
			if data, err := json.Marshal(rtc.NativeArgs); err == nil {
				return string(data)
			}
		}
		return "{}"
	}
	raw := strings.TrimSpace(rtc.RawArgs)
	if raw == "" {
		return "{}"
	}
	return raw
}

// executePluginTool routes one granted plugin call through the registered
// runner, with the standard truncation policy.
func executePluginTool(ctx context.Context, v validatedTC) execResult {
	runner := currentPluginToolRunner()
	name := v.rtc.pluginName
	if runner == nil {
		err := fmt.Errorf("%s is not available in this session", name)
		record := ToolCallRecord{Name: name, Args: v.rtc.RawArgs, Error: err}
		return execResult{index: v.index, record: record, output: fmt.Sprintf("[%s] %v\n", name, err), failed: true, toolID: v.rtc.ID}
	}
	output, err := runner(ctx, name, pluginEnvelopeJSON(v.rtc))
	if err != nil {
		record := ToolCallRecord{Name: name, Args: v.rtc.RawArgs, Error: err}
		return execResult{index: v.index, record: record, output: fmt.Sprintf("[%s] %v\n", name, err), failed: true, toolID: v.rtc.ID}
	}
	output = TruncateToolResult(name, output)
	record := ToolCallRecord{Name: name, Args: v.rtc.RawArgs, Output: output}
	return execResult{index: v.index, record: record, output: fmt.Sprintf("[%s] %s\n", name, output), toolID: v.rtc.ID}
}
