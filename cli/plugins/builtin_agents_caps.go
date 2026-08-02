/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"encoding/json"
	"strings"

	"github.com/diillson/chatcli/i18n"
)

// agentsSubcommand resolves the effective subcommand from raw plugin args
// without fully parsing the payload (capability checks must stay cheap).
func agentsSubcommand(args []string) string {
	if len(args) == 0 {
		return "list"
	}
	first := strings.TrimSpace(args[0])
	if strings.HasPrefix(first, "{") {
		var top struct {
			Cmd     string `json:"cmd"`
			Command string `json:"command"`
		}
		if err := json.Unmarshal([]byte(first), &top); err == nil {
			if top.Cmd != "" {
				return top.Cmd
			}
			if top.Command != "" {
				return top.Command
			}
		}
		return "list"
	}
	return first
}

// IsReadOnly: list/show only observe; cancel mutates run state.
func (p *BuiltinAgentsPlugin) IsReadOnly(args []string) bool {
	switch agentsSubcommand(args) {
	case "cancel", "stop", "kill":
		return false
	}
	return true
}

// IsConcurrencySafe: observation is safe to run alongside anything; cancel
// is serialized like other mutating tools.
func (p *BuiltinAgentsPlugin) IsConcurrencySafe(args []string) bool {
	return p.IsReadOnly(args)
}

// DescribeCall renders the spinner label for a call.
func (p *BuiltinAgentsPlugin) DescribeCall(args []string) string {
	switch agentsSubcommand(args) {
	case "show", "get", "info":
		return i18n.T("plugins.agents.describe.show")
	case "cancel", "stop", "kill":
		return i18n.T("plugins.agents.describe.cancel")
	default:
		return i18n.T("plugins.agents.describe.list")
	}
}

// JSONSchema provides strict validation for native tool calls.
func (p *BuiltinAgentsPlugin) JSONSchema() string {
	schema := map[string]interface{}{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type":    "object",
		"oneOf": []map[string]interface{}{
			{
				"properties": map[string]interface{}{
					"cmd": map[string]interface{}{"const": "list"},
					"args": map[string]interface{}{
						"type":                 "object",
						"additionalProperties": false,
					},
				},
				"required": []string{"cmd"},
			},
			{
				"properties": map[string]interface{}{
					"cmd": map[string]interface{}{"enum": []string{"show", "cancel"}},
					"args": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"id": map[string]interface{}{"type": "string", "minLength": 1},
						},
						"required": []string{"id"},
					},
				},
				"required": []string{"cmd", "args"},
			},
		},
	}
	data, _ := json.Marshal(schema)
	return string(data)
}
