/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import "github.com/diillson/chatcli/i18n"

// Capability advertisements for BuiltinSearchPlugin.

// IsReadOnly reports true: @search only reads files, never modifies them.
func (p *BuiltinSearchPlugin) IsReadOnly(_ []string) bool { return true }

// IsConcurrencySafe reports true: multiple searches don't conflict.
// File system reads at the syscall layer are safe to interleave.
func (p *BuiltinSearchPlugin) IsConcurrencySafe(_ []string) bool { return true }

// DescribeCall surfaces the regex pattern being searched.
func (p *BuiltinSearchPlugin) DescribeCall(args []string) string {
	parsed, err := parseSearchArgs(args)
	if err != nil || parsed.Term == "" {
		return p.Description()
	}
	t := parsed.Term
	if len(t) > 60 {
		t = t[:60] + "..."
	}
	return i18n.T("plugins.search.describe", t)
}

// MaxResultChars raises the per-call truncation cap for @search.
// grep output is structured (file:line:match) and the LLM needs
// breadth more than depth; cutting at the global 30k blocks many
// repository-wide investigations. 60k chars (~15k tokens with
// standard BPE) is a pragmatic upper bound for one search call.
func (p *BuiltinSearchPlugin) MaxResultChars() int { return 60_000 }

// JSONSchema returns the draft-2020-12 schema for @search input.
func (p *BuiltinSearchPlugin) JSONSchema() string {
	return `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "object",
		"properties": {
			"term": {"type": "string", "minLength": 1},
			"pattern": {"type": "string", "minLength": 1},
			"query": {"type": "string", "minLength": 1},
			"regex": {"type": "string", "minLength": 1},
			"dir": {"type": "string"},
			"path": {"type": "string"},
			"directory": {"type": "string"},
			"max_results": {"type": "integer", "minimum": 1},
			"maxResults": {"type": "integer", "minimum": 1},
			"limit": {"type": "integer", "minimum": 1},
			"include": {"type": "string"},
			"glob": {"type": "string"}
		},
		"anyOf": [
			{"required": ["term"]},
			{"required": ["pattern"]},
			{"required": ["query"]},
			{"required": ["regex"]}
		]
	}`
}
