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

// IsReadOnly: history and peek observe; send enqueues and inbox drains
// (mutating).
func (p *BuiltinMailPlugin) IsReadOnly(args []string) bool {
	switch mailSubcommand(args) {
	case "history", "recent", "log", "peek", "look", "pending":
		return true
	}
	return false
}

// IsConcurrencySafe mirrors IsReadOnly: history is a read; send enqueues
// and inbox drains, and their order matters (a reply after the message it
// answers), so they stay serial.
func (p *BuiltinMailPlugin) IsConcurrencySafe(args []string) bool { return p.IsReadOnly(args) }

// DescribeCall renders the spinner label for a call.
func (p *BuiltinMailPlugin) DescribeCall(args []string) string {
	switch mailSubcommand(args) {
	case "send", "post":
		return i18n.T("plugins.mail.describe.send")
	case "history", "recent", "log":
		return i18n.T("plugins.mail.describe.history")
	case "peek", "look", "pending":
		recipient := extractNestedArg(args, "for", "recipient", "to", "agent")
		if recipient == "" {
			recipient = mailDefaultRecipient
		}
		return i18n.T("plugins.mail.describe.peek", recipient)
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
					"cmd": map[string]interface{}{"enum": []string{"inbox", "peek", "history"}},
					"args": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"limit": map[string]interface{}{"type": "integer", "minimum": 1},
							"for":   map[string]interface{}{"type": "string"},
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
