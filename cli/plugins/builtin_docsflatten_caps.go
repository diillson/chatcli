/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import "github.com/diillson/chatcli/i18n"

// Capability advertisements for BuiltinDocsFlattenPlugin.

// IsReadOnly is true only for purely local flattens that return their result
// inline: a clone executes git against the network and an output path writes
// a file, so both keep the security confirmation in the loop.
func (p *BuiltinDocsFlattenPlugin) IsReadOnly(args []string) bool {
	cfg, err := parseDocsFlattenArgs(args)
	if err != nil {
		return false
	}
	return cfg.Repo == "" && cfg.Output == ""
}

// IsConcurrencySafe mirrors IsReadOnly: local inline flattens can fan out,
// anything that clones or writes runs serially.
func (p *BuiltinDocsFlattenPlugin) IsConcurrencySafe(args []string) bool {
	return p.IsReadOnly(args)
}

// DescribeCall surfaces the corpus being flattened in the spinner.
func (p *BuiltinDocsFlattenPlugin) DescribeCall(args []string) string {
	if v := extractStringArg(args, "repo", "repoUrl", "url"); v != "" {
		return i18n.T("plugins.docsflatten.describe", v)
	}
	if v := extractStringArg(args, "root", "dir", "path"); v != "" {
		return i18n.T("plugins.docsflatten.describe", v)
	}
	return p.Description()
}

// MaxResultChars raises the inline cap: a flattened corpus is dense reference
// material and the schema already pushes large runs to --output, so what does
// come back inline deserves more room than the 30k default.
func (p *BuiltinDocsFlattenPlugin) MaxResultChars() int { return 50_000 }

// JSONSchema returns the draft-2020-12 schema for @docs-flatten input.
func (p *BuiltinDocsFlattenPlugin) JSONSchema() string {
	return `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "object",
		"properties": {
			"root": {"type": "string"},
			"repo": {"type": "string"},
			"branch": {"type": "string"},
			"subdir": {"type": "string"},
			"format": {"type": "string", "enum": ["text", "jsonl", "json", "yaml"]},
			"maxChars": {"type": "integer", "minimum": 0},
			"include": {"type": "string"},
			"exclude": {"type": "string"},
			"stripFrontMatter": {"type": "boolean"},
			"output": {"type": "string"}
		}
	}`
}
