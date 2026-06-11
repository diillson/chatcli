/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"encoding/json"

	"github.com/diillson/chatcli/i18n"
)

// Capability advertisements for BuiltinKnowledgePlugin, in the same
// per-plugin caps file shape as @read/@park/@todo.

// IsReadOnly reports true for every invocation: @knowledge only queries the
// attached knowledge bases — it never mutates files or state. The
// orchestrator uses this to skip the security prompt.
func (p *BuiltinKnowledgePlugin) IsReadOnly(_ []string) bool { return true }

// IsConcurrencySafe reports true: every subcommand reads immutable, cached
// corpus data, so parallel searches over different topics never conflict.
func (p *BuiltinKnowledgePlugin) IsConcurrencySafe(_ []string) bool { return true }

// DescribeCall surfaces what is being looked up so the spinner reads
// "Searching knowledge base: \"gateway env vars\"" instead of the static
// (long) description or the raw JSON envelope — both of which overflow a
// terminal row and break the spinner's single-line repaint.
func (p *BuiltinKnowledgePlugin) DescribeCall(args []string) string {
	cmd, inner, err := parseKnowledgeInvocation(args)
	if err != nil {
		return i18n.T("plugins.knowledge.describe_generic")
	}
	switch cmd {
	case "search":
		var in struct {
			Query string `json:"query"`
		}
		_ = json.Unmarshal([]byte(inner), &in)
		if in.Query == "" {
			return i18n.T("plugins.knowledge.describe_generic")
		}
		return i18n.T("plugins.knowledge.describe_search", describeTrim(in.Query))
	case "get":
		var in struct {
			Source string `json:"source"`
		}
		_ = json.Unmarshal([]byte(inner), &in)
		if in.Source == "" {
			return i18n.T("plugins.knowledge.describe_generic")
		}
		return i18n.T("plugins.knowledge.describe_get", describeTrim(in.Source))
	case "toc":
		return i18n.T("plugins.knowledge.describe_toc")
	case "list":
		return i18n.T("plugins.knowledge.describe_list")
	}
	return i18n.T("plugins.knowledge.describe_generic")
}

// describeTrim bounds one user-supplied value inside a spinner label;
// rune-aware so multibyte queries truncate cleanly.
func describeTrim(s string) string {
	const limit = 48
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit-1]) + "…"
}
