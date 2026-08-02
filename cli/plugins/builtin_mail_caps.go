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

// mailSubcommand resolves the effective subcommand from raw plugin args.
func mailSubcommand(args []string) string {
	if len(args) == 0 {
		return "inbox"
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
		return "inbox"
	}
	return first
}

// IsReadOnly: history observes; send enqueues and inbox drains (mutating).
func (p *BuiltinMailPlugin) IsReadOnly(args []string) bool {
	switch mailSubcommand(args) {
	case "history", "recent", "log":
		return true
	}
	return false
}

// IsConcurrencySafe: the bus is an in-memory mutex-serialized structure —
// every operation is instantaneous and safe alongside other tools.
func (p *BuiltinMailPlugin) IsConcurrencySafe(_ []string) bool { return true }

// DescribeCall renders the spinner label for a call.
func (p *BuiltinMailPlugin) DescribeCall(args []string) string {
	switch mailSubcommand(args) {
	case "send", "post":
		return i18n.T("plugins.mail.describe.send")
	case "history", "recent", "log":
		return i18n.T("plugins.mail.describe.history")
	default:
		return i18n.T("plugins.mail.describe.inbox")
	}
}

// JSONSchema provides strict validation for native tool calls.
func (p *BuiltinMailPlugin) JSONSchema() string {
	schema := map[string]interface{}{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type":    "object",
		"oneOf": []map[string]interface{}{
			{
				"properties": map[string]interface{}{
					"cmd": map[string]interface{}{"enum": []string{"inbox", "history"}},
					"args": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"limit": map[string]interface{}{"type": "integer", "minimum": 1},
						},
					},
				},
				"required": []string{"cmd"},
			},
			{
				"properties": map[string]interface{}{
					"cmd": map[string]interface{}{"const": "send"},
					"args": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"to":      map[string]interface{}{"type": "string", "minLength": 1},
							"text":    map[string]interface{}{"type": "string", "minLength": 1},
							"card_id": map[string]interface{}{"type": "string"},
						},
						"required": []string{"to", "text"},
					},
				},
				"required": []string{"cmd", "args"},
			},
		},
	}
	data, _ := json.Marshal(schema)
	return string(data)
}
