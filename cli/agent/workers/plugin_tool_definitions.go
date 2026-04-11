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
	"fmt"

	"github.com/diillson/chatcli/models"
)

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
				Description: "Fetch the content of a web page and return its text (HTML stripped). Use this to read documentation, articles, or any web content.",
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
							"description": "Maximum content length in characters (default: 50000)",
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
			result := []string{"fetch"}
			if u, ok := args["url"].(string); ok && u != "" {
				result = append(result, "--url", u)
			}
			if raw, ok := args["raw"].(bool); ok && raw {
				result = append(result, "--raw")
			}
			if ml, ok := args["max_length"].(float64); ok && ml > 0 {
				result = append(result, "--maxLength", fmt.Sprintf("%d", int(ml)))
			}
			return result
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
