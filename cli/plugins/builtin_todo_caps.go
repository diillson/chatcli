/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"strings"

	"github.com/diillson/chatcli/i18n"
)

// Capability advertisements for BuiltinTodoPlugin. The semantics
// depend on the subcommand: list is read-only; write and mark mutate
// the tracker's in-process state. The orchestrator's partition policy
// uses these to decide whether multiple @todo calls can batch.

// IsReadOnly returns true only for the list subcommand. write and
// mark mutate the in-process tracker; even though the change is
// confined to the agent loop's own state (no disk side-effect), the
// orchestrator should treat them as serial-only so per-task ordering
// stays well-defined.
func (p *BuiltinTodoPlugin) IsReadOnly(args []string) bool {
	sub := todoSubcommand(args)
	return sub == "list" || sub == ""
}

// IsConcurrencySafe mirrors IsReadOnly. Two parallel list calls don't
// interfere; two parallel writes would race on the tracker mutex and
// produce undefined "which write wins" semantics that we want to
// surface deterministically (last call wins, but the user can audit
// the call order in serial execution).
func (p *BuiltinTodoPlugin) IsConcurrencySafe(args []string) bool {
	return p.IsReadOnly(args)
}

// DescribeCall surfaces the subcommand for the spinner. write and
// mark also surface the relevant identifier when present.
func (p *BuiltinTodoPlugin) DescribeCall(args []string) string {
	sub := todoSubcommand(args)
	switch sub {
	case "write":
		return i18n.T("plugins.todo.describe.write")
	case "mark":
		// Include the id when easily extractable.
		if id := extractStringArg(args, "id"); id != "" {
			return i18n.T("plugins.todo.describe.mark_id", id)
		}
		return i18n.T("plugins.todo.describe.mark")
	case "list", "":
		return i18n.T("plugins.todo.describe.list")
	}
	return p.Description()
}

// todoSubcommand pulls the subcommand from either the JSON envelope
// or the first positional token. Mirrors the helpers in @coder /
// @park / @scheduler.
func todoSubcommand(args []string) string {
	if len(args) == 0 {
		return ""
	}
	first := strings.TrimSpace(args[0])
	if strings.HasPrefix(first, "{") {
		if v := extractStringArg([]string{first}, "cmd", "command"); v != "" {
			return v
		}
	}
	return first
}

// JSONSchema returns the draft-2020-12 schema for @todo input. Three
// subcommand shapes are enumerated via oneOf so the validator rejects
// {"cmd":"write"} without args (the LLM's most common mistake when
// learning the tool).
func (p *BuiltinTodoPlugin) JSONSchema() string {
	return `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"oneOf": [
			{
				"type": "object",
				"properties": {
					"cmd": {"const": "list"}
				},
				"required": ["cmd"],
				"additionalProperties": true
			},
			{
				"type": "object",
				"properties": {
					"cmd": {"const": "write"},
					"args": {
						"type": "object",
						"properties": {
							"todos": {
								"type": "array",
								"minItems": 1,
								"items": {
									"type": "object",
									"properties": {
										"description": {"type": "string", "minLength": 1},
										"status": {
											"type": "string",
											"enum": ["", "pending", "in_progress", "completed", "failed"]
										}
									},
									"required": ["description"]
								}
							}
						},
						"required": ["todos"]
					}
				},
				"required": ["cmd", "args"]
			},
			{
				"type": "object",
				"properties": {
					"cmd": {"const": "mark"},
					"args": {
						"type": "object",
						"properties": {
							"id": {"type": "integer", "minimum": 1},
							"status": {
								"type": "string",
								"enum": ["pending", "in_progress", "completed", "failed"]
							},
							"error": {"type": "string"}
						},
						"required": ["id", "status"]
					}
				},
				"required": ["cmd", "args"]
			}
		]
	}`
}
