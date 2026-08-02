/*
 * ChatCLI - Plugin Tool Definitions for Native Function Calling
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Generates native tool definitions for built-in plugins (@websearch, @webfetch)
 * so that providers with native function calling can use structured tool calls
 * instead of XML parsing. This eliminates phantom tool call detection issues
 * and provides better reliability.
 */
package workers

import (
	"encoding/json"
	"fmt"

	"github.com/diillson/chatcli/cli/agent/ask"
	"github.com/diillson/chatcli/models"
)

// jsonMarshalForTool marshals v with compact encoding; the tool calls accept
// single-line JSON only. Errors are silently swallowed (returning []byte{"{}"})
// because the LLM will just re-try with corrected args.
func jsonMarshalForTool(v interface{}) ([]byte, error) {
	out, err := json.Marshal(v)
	if err != nil {
		return []byte(`{}`), err
	}
	return out, nil
}

// PluginToolDefinitions returns native tool definitions for built-in agent plugins.
// These are used alongside CoderToolDefinitions when native tool calling is available.
func PluginToolDefinitions() []models.ToolDefinition {
	return []models.ToolDefinition{
		{
			Type: "function",
			Function: models.ToolFunctionDef{
				Name:        "web_search",
				Description: "Search the web using DuckDuckGo and return results with titles, URLs, and snippets. Use this to find current information, documentation, or answers to questions.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"query": map[string]interface{}{
							"type":        "string",
							"description": "The search query",
						},
						"max_results": map[string]interface{}{
							"type":        "integer",
							"description": "Maximum number of results to return (default: 10)",
						},
					},
					"required": []string{"query"},
				},
			},
		},
		{
			Type: "function",
			Function: models.ToolFunctionDef{
				Name:        "web_fetch",
				Description: "Fetch a web page or HTTP endpoint and return its text (HTML stripped). Prefer filter/from_line/to_line to scope output when possible. Bodies larger than ~10KB without a filter are auto-saved to the session scratch dir; you'll get a short preview plus the absolute path and should use read_file with start/end (or rerun web_fetch with a filter) for specific ranges.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"url": map[string]interface{}{
							"type":        "string",
							"description": "The URL to fetch",
						},
						"raw": map[string]interface{}{
							"type":        "boolean",
							"description": "Return raw HTML instead of stripped text (default: false)",
						},
						"max_length": map[string]interface{}{
							"type":        "integer",
							"description": "Maximum returned-inline length in characters (default: 20000). Larger bodies are auto-saved to disk.",
						},
						"filter": map[string]interface{}{
							"type":        "string",
							"description": "Keep only lines matching this regex (Go syntax). Example: '^chatcli_' to narrow a Prometheus metrics scrape.",
						},
						"exclude": map[string]interface{}{
							"type":        "string",
							"description": "Drop lines matching this regex. Applied after filter.",
						},
						"from_line": map[string]interface{}{
							"type":        "integer",
							"description": "Start at this line (1-based). Applied after filter/exclude.",
						},
						"to_line": map[string]interface{}{
							"type":        "integer",
							"description": "End at this line (1-based). Applied after filter/exclude.",
						},
						"save_to_file": map[string]interface{}{
							"type":        "boolean",
							"description": "Save the FULL pre-filter response to the session scratch dir. Returns preview+path so you can read_file specific ranges later. Use this explicitly when you know the body is large; otherwise the auto-save threshold triggers automatically.",
						},
						"save_path": map[string]interface{}{
							"type":        "string",
							"description": "Override save filename (relative path is placed under CHATCLI_AGENT_TMPDIR).",
						},
					},
					"required": []string{"url"},
				},
			},
		},
		AskUserToolDefinition(),
		{
			Type: "function",
			Function: models.ToolFunctionDef{
				Name: "squad_mail",
				Description: "Squad messaging for the orchestrator: send a directed message to a worker agent (delivered at its next turn), drain your own inbox, or audit recent traffic. " +
					"Use send to redirect a running worker (e.g. after a review verdict) instead of waiting for it to finish.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"cmd": map[string]interface{}{
							"type":        "string",
							"enum":        []string{"send", "inbox", "history"},
							"description": "mail operation",
						},
						"to":      map[string]interface{}{"type": "string", "description": "Recipient worker agent type (send)."},
						"text":    map[string]interface{}{"type": "string", "description": "Message text (send)."},
						"card_id": map[string]interface{}{"type": "string", "description": "Board card this message is about (send)."},
						"limit":   map[string]interface{}{"type": "integer", "description": "Max messages to show (history)."},
					},
					"required": []string{"cmd"},
				},
			},
		},
		{
			Type: "function",
			Function: models.ToolFunctionDef{
				Name: "agents_runs",
				Description: "Observe and manage live agent executions (the squad): list running workers/subagents/MoA members with their current turn and action, show one run's detail, or cancel a stuck run. " +
					"Use after dispatching <agent_call> workers to monitor progress, and cancel runs that are looping without progress.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"cmd": map[string]interface{}{
							"type":        "string",
							"enum":        []string{"list", "show", "cancel"},
							"description": "list = all live + recent runs; show = one run's detail; cancel = request cancellation of a live run",
						},
						"id": map[string]interface{}{
							"type":        "string",
							"description": "Run ID (e.g. run-3). Required for show and cancel.",
						},
					},
					"required": []string{"cmd"},
				},
			},
		},
		{
			Type: "function",
			Function: models.ToolFunctionDef{
				Name: "board_cards",
				Description: "Manage the squad work board (kanban: backlog, doing, review, blocked, done). Break the user's goal into cards, assign each to a worker agent type, move cards as work progresses, record review verdicts and delivery notes, link agent runs and scheduler jobs. " +
					"Use it to coordinate multi-step deliveries end to end without asking the user to track anything.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"cmd": map[string]interface{}{
							"type":        "string",
							"enum":        []string{"create", "list", "show", "move", "assign", "note", "link", "archive"},
							"description": "board operation",
						},
						"id":          map[string]interface{}{"type": "string", "description": "Card ID (e.g. card-3). Required for show/move/assign/note/link."},
						"title":       map[string]interface{}{"type": "string", "description": "Card title (create)."},
						"description": map[string]interface{}{"type": "string", "description": "Full task description / acceptance criteria (create)."},
						"assignee":    map[string]interface{}{"type": "string", "description": "Worker agent type: coder, reviewer, tester, … (create/assign)."},
						"column":      map[string]interface{}{"type": "string", "description": "Column filter (list) or initial column (create)."},
						"to":          map[string]interface{}{"type": "string", "description": "Target column (move)."},
						"text":        map[string]interface{}{"type": "string", "description": "Note text (note)."},
						"author":      map[string]interface{}{"type": "string", "description": "Note author (note); defaults to orchestrator."},
						"run_id":      map[string]interface{}{"type": "string", "description": "Agent run ID to link (link)."},
						"job_id":      map[string]interface{}{"type": "string", "description": "Scheduler job ID to link (link)."},
						"older_than":  map[string]interface{}{"type": "string", "description": "Archive only done cards older than this Go duration (archive)."},
					},
					"required": []string{"cmd"},
				},
			},
		},
	}
}

// AskUserToolDefinition is the native tool definition for ask_user (the @ask
// plugin). It is also offered standalone in chat mode (the only tool chat is
// allowed to use), so it lives in its own constructor.
func AskUserToolDefinition() models.ToolDefinition {
	var params map[string]interface{}
	_ = json.Unmarshal(ask.ParametersJSON(), &params)
	return models.ToolDefinition{
		Type: "function",
		Function: models.ToolFunctionDef{
			Name: "ask_user",
			Description: "Ask the user 1-6 multiple-choice questions and get their decisions. " +
				"Use when you need the user to choose between options before proceeding. Each question " +
				"has a header, options (label+description), single or multi-select, and an implicit " +
				"free-text 'Other' choice. Returns the user's selections.",
			Parameters: params,
		},
	}
}

// nativePluginToolMap maps native function names to plugin names and argument builders.
var nativePluginToolMap = map[string]struct {
	PluginName string
	BuildArgs  func(args map[string]interface{}) []string
}{
	"web_search": {
		PluginName: "@websearch",
		BuildArgs: func(args map[string]interface{}) []string {
			result := []string{"search"}
			if q, ok := args["query"].(string); ok && q != "" {
				result = append(result, "--query", q)
			}
			if mr, ok := args["max_results"].(float64); ok && mr > 0 {
				result = append(result, "--maxResults", fmt.Sprintf("%d", int(mr)))
			}
			return result
		},
	},
	"web_fetch": {
		PluginName: "@webfetch",
		BuildArgs: func(args map[string]interface{}) []string {
			// Use the single-JSON-arg form so the webfetch plugin sees the
			// exact map the LLM sent (easier than threading every flag).
			payload := map[string]interface{}{"cmd": "fetch", "args": args}
			raw, _ := jsonMarshalForTool(payload)
			return []string{string(raw)}
		},
	},
	"squad_mail": {
		PluginName: "@mail",
		BuildArgs: func(args map[string]interface{}) []string {
			payload := map[string]interface{}{"cmd": args["cmd"], "args": args}
			raw, _ := jsonMarshalForTool(payload)
			return []string{string(raw)}
		},
	},
	"board_cards": {
		PluginName: "@board",
		BuildArgs: func(args map[string]interface{}) []string {
			payload := map[string]interface{}{"cmd": args["cmd"], "args": args}
			raw, _ := jsonMarshalForTool(payload)
			return []string{string(raw)}
		},
	},
	"agents_runs": {
		PluginName: "@agents",
		BuildArgs: func(args map[string]interface{}) []string {
			payload := map[string]interface{}{"cmd": args["cmd"], "args": args}
			raw, _ := jsonMarshalForTool(payload)
			return []string{string(raw)}
		},
	},
	"ask_user": {
		PluginName: "@ask",
		BuildArgs: func(args map[string]interface{}) []string {
			// Pass the {questions:[...]} object straight through as a single
			// JSON arg; the @ask plugin / loop parse it with ask.ParseRequest.
			raw, _ := jsonMarshalForTool(args)
			return []string{string(raw)}
		},
	},
}

// IsNativePluginTool checks if a native tool function name maps to a plugin.
func IsNativePluginTool(funcName string) bool {
	_, ok := nativePluginToolMap[funcName]
	return ok
}

// ResolveNativePluginTool converts a native tool call to plugin name + CLI args.
// Returns (pluginName, args, true) if resolved, or ("", nil, false) if not a plugin tool.
func ResolveNativePluginTool(funcName string, arguments map[string]interface{}) (string, []string, bool) {
	mapping, ok := nativePluginToolMap[funcName]
	if !ok {
		return "", nil, false
	}
	return mapping.PluginName, mapping.BuildArgs(arguments), true
}
