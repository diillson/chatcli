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

// Capability advertisements for BuiltinLSPPlugin, in the same per-plugin caps
// file shape as @knowledge/@read/@park.

// IsReadOnly reports true for every invocation: @lsp only queries language
// servers about source files — it never edits code or state. (The servers it
// launches are curated presets from the LSP registry, overridable only via
// the operator's CHATCLI_LSP_*_CMD env vars, not by tool arguments.)
func (p *BuiltinLSPPlugin) IsReadOnly(_ []string) bool { return true }

// IsConcurrencySafe reports true: the session pool serializes access to each
// language server behind its own lock, so parallel queries never conflict.
func (p *BuiltinLSPPlugin) IsConcurrencySafe(_ []string) bool { return true }

// DescribeCall surfaces what is being navigated so the spinner reads
// "LSP: references of cli/cli.go:128" instead of the raw JSON envelope.
func (p *BuiltinLSPPlugin) DescribeCall(args []string) string {
	cmd, inner, err := parseLSPInvocation(args)
	if err != nil {
		return i18n.T("plugins.lsp.describe_generic")
	}
	var in struct {
		File string `json:"file"`
		Line int    `json:"line"`
	}
	_ = json.Unmarshal([]byte(inner), &in)
	if in.File == "" {
		return i18n.T("plugins.lsp.describe_generic")
	}
	target := describeTrim(in.File)
	switch cmd {
	case "diagnostics":
		return i18n.T("plugins.lsp.describe_diagnostics", target)
	case "definition":
		return i18n.T("plugins.lsp.describe_definition", target, in.Line)
	case "references":
		return i18n.T("plugins.lsp.describe_references", target, in.Line)
	case "symbols":
		return i18n.T("plugins.lsp.describe_symbols", target)
	case "hover":
		return i18n.T("plugins.lsp.describe_hover", target, in.Line)
	default:
		return i18n.T("plugins.lsp.describe_generic")
	}
}
