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

// boardSubcommand resolves the effective subcommand from raw plugin args
// without fully parsing the payload.
func boardSubcommand(args []string) string {
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

// IsReadOnly: list/show observe; everything else mutates the board.
func (p *BuiltinBoardPlugin) IsReadOnly(args []string) bool {
	switch boardSubcommand(args) {
	case "list", "ls", "status", "show", "get", "info":
		return true
	}
	return false
}

// IsConcurrencySafe mirrors IsReadOnly: reads can run alongside anything.
func (p *BuiltinBoardPlugin) IsConcurrencySafe(args []string) bool {
	return p.IsReadOnly(args)
}

// DescribeCall renders the spinner label for a call.
func (p *BuiltinBoardPlugin) DescribeCall(args []string) string {
	switch boardSubcommand(args) {
	case "create", "add", "new":
		return i18n.T("plugins.board.describe.create")
	case "show", "get", "info":
		return i18n.T("plugins.board.describe.show")
	case "move", "transition":
		return i18n.T("plugins.board.describe.move")
	case "assign":
		return i18n.T("plugins.board.describe.assign")
	case "note", "comment":
		return i18n.T("plugins.board.describe.note")
	case "link":
		return i18n.T("plugins.board.describe.link")
	case "archive", "gc", "clean":
		return i18n.T("plugins.board.describe.archive")
	default:
		return i18n.T("plugins.board.describe.list")
	}
}

// JSONSchema provides strict validation for native tool calls.
func (p *BuiltinBoardPlugin) JSONSchema() string {
	str := func() map[string]interface{} { return map[string]interface{}{"type": "string"} }
	req := func() map[string]interface{} {
		return map[string]interface{}{"type": "string", "minLength": 1}
	}
	schema := map[string]interface{}{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type":    "object",
		"oneOf": []map[string]interface{}{
			{
				"properties": map[string]interface{}{
					"cmd": map[string]interface{}{"enum": []string{"list", "archive"}},
					"args": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"column":     str(),
							"older_than": str(),
						},
					},
				},
				"required": []string{"cmd"},
			},
			{
				"properties": map[string]interface{}{
					"cmd": map[string]interface{}{"const": "create"},
					"args": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"title":       req(),
							"description": str(),
							"assignee":    str(),
							"column":      str(),
						},
						"required": []string{"title"},
					},
				},
				"required": []string{"cmd", "args"},
			},
			{
				"properties": map[string]interface{}{
					"cmd": map[string]interface{}{"enum": []string{"show", "move", "assign", "note", "link"}},
					"args": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"id":       req(),
							"to":       str(),
							"assignee": str(),
							"text":     str(),
							"author":   str(),
							"run_id":   str(),
							"job_id":   str(),
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
