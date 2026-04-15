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
				Description: "Fetch a web page or HTTP endpoint and return its text (HTML stripped). Supports line-level regex filtering (filter, exclude) and line-range slicing (from_line, to_line) to handle large payloads like Prometheus /metrics without blowing up the context. For very large responses, set save_to_file=true to persist the full body to the session scratch dir and receive preview+path back — then use read_file to examine specific ranges on demand.",
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
							"description": "Maximum returned-inline length in characters (default: 50000).",
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
							"description": "Save the FULL pre-filter response to the session scratch dir. Returns preview+path so you can read_file specific ranges later. Use for responses >50k chars.",
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
