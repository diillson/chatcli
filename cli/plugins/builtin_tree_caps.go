/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import "github.com/diillson/chatcli/i18n"

// Capability advertisements for BuiltinTreePlugin.

// IsReadOnly reports true: directory listing never mutates the tree.
func (p *BuiltinTreePlugin) IsReadOnly(_ []string) bool { return true }

// IsConcurrencySafe reports true: multiple tree walks don't conflict.
func (p *BuiltinTreePlugin) IsConcurrencySafe(_ []string) bool { return true }

// DescribeCall surfaces the directory being listed.
func (p *BuiltinTreePlugin) DescribeCall(args []string) string {
	parsed, err := parseTreeArgs(args)
	if err != nil {
		return p.Description()
	}
	dir := parsed.Dir
	if dir == "" {
		dir = "."
	}
	return i18n.T("plugins.tree.describe", dir)
}

// JSONSchema returns the draft-2020-12 schema for @tree input. All
// fields are optional — bare {} defaults to listing the current
// workspace.
func (p *BuiltinTreePlugin) JSONSchema() string {
	return `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "object",
		"properties": {
			"dir": {"type": "string"},
			"path": {"type": "string"},
			"directory": {"type": "string"},
			"depth": {"type": "integer", "minimum": 1, "maximum": 20},
			"maxDepth": {"type": "integer", "minimum": 1, "maximum": 20},
			"exclude": {"type": "string"},
			"skip": {"type": "string"}
		}
	}`
}
