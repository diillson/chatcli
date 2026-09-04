/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/diillson/chatcli/cli/agent/workers"
	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// Deferred tool definitions, CLI side.
//
// workers.PlanToolDefs decides what a turn ships; this file supplies the
// threshold it decides against and answers the search call the model makes
// when it wants a schema the turn did not carry.

// toolDeferThreshold is the serialized tool payload this run tolerates
// before deferring, derived from the model's context window so a large
// window keeps more tools loaded.
func (a *AgentMode) toolDeferThreshold() int {
	window := 0
	if a.cli != nil {
		window = catalog.GetContextWindow(a.cli.Provider, a.cli.Model)
	}
	charsPerToken := int(defaultCharsPerToken)
	if a.cli != nil {
		if cpt, samples := a.cli.calibrator().CharsPerToken(a.cli.Provider, a.cli.Model); samples > 0 && cpt > 0 {
			charsPerToken = int(cpt)
		}
	}
	return workers.DeferThresholdChars(window, charsPerToken)
}

// handleFindTools answers a find_tools call: it resolves the query against
// the tools this run deferred, marks the matches as activated so they
// travel on every later turn, and returns their definitions as the tool
// result.
//
// Returns ok=false when the call is not find_tools, so the caller can fall
// through to its normal dispatch.
func (a *AgentMode) handleFindTools(call models.ToolCall) (string, bool) {
	if call.Name != workers.FindToolsName {
		return "", false
	}
	query, _ := call.Arguments["query"].(string)
	matches := workers.SearchToolDefs(a.deferrableTools, query, 5)
	if len(matches) == 0 {
		// An empty result is a real answer, not an error: the model asked
		// for something the index does not hold and should stop looking.
		return "No tool in the index matches " + strconv.Quote(strings.TrimSpace(query)) +
			". Use the tools already loaded, or tell the user what is missing.", true
	}
	if a.activatedTools == nil {
		a.activatedTools = map[string]bool{}
	}
	names := make([]string, 0, len(matches))
	for _, m := range matches {
		a.activatedTools[m.Function.Name] = true
		names = append(names, m.Function.Name)
	}
	payload, err := json.Marshal(matches)
	if err != nil {
		return "Loaded: " + strings.Join(names, ", "), true
	}
	a.logger.Info("tool search loaded deferred definitions",
		zap.Strings("tools", names),
		zap.Int("deferred_remaining", len(a.deferrableTools)-len(a.activatedTools)))
	return "Loaded and now callable: " + strings.Join(names, ", ") + "\n" + string(payload), true
}

// prepareDeferredTools captures the run's deferrable set and returns the
// index to advertise, or "" when the payload is small enough that every
// schema travels as before.
//
// Called once while the cached prompt is assembled, so the index is fixed
// for the run: activating a tool changes which schemas travel, never the
// prefix the cache is keyed on.
func (a *AgentMode) prepareDeferredTools() string {
	a.activatedTools = map[string]bool{}
	a.deferrableTools = nil
	if a.cli == nil || a.cli.mcpManager == nil || a.cli.mcpManager.ToolCount() == 0 {
		return ""
	}
	var core []models.ToolDefinition
	if a.isCoderMode {
		core = workers.CoderToolDefinitions(nil)
	}
	core = append(core, workers.PluginToolDefinitions()...)

	deferrable := a.cli.mcpManager.GetTools()
	plan := workers.PlanToolDefs(core, deferrable, nil, a.toolDeferThreshold())
	if plan.Deferred == 0 {
		return ""
	}
	a.deferrableTools = deferrable
	a.logger.Info("deferring tool definitions behind a searchable index",
		zap.Int("deferred", plan.Deferred),
		zap.Int("loaded", len(plan.Defs)))
	return plan.Index
}
