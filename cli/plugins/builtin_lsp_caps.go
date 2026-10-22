/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"github.com/diillson/chatcli/i18n"
)

// Capability advertisements for BuiltinLSPPlugin, in the same per-plugin caps
// file shape as @knowledge/@read/@park.

// lspMutates reports whether an invocation writes to disk: only a rename
// with dry_run:false does. A parse failure counts as mutating (fail closed).
func lspMutates(args []string) bool {
	cmd, inner, err := parseLSPInvocation(args)
	if err != nil {
		return true
	}
	return cmd == "rename" && !decodeLSPRaw(inner).DryRun
}

// IsReadOnly reports true for every query: @lsp only asks language servers
// about source files. (The servers it launches are curated presets from the
// LSP registry, overridable only via the operator's CHATCLI_LSP_*_CMD env
// vars, not by tool arguments.) The single exception is rename with
// dry_run:false, which rewrites files.
func (p *BuiltinLSPPlugin) IsReadOnly(args []string) bool { return !lspMutates(args) }

// IsConcurrencySafe mirrors IsReadOnly: the session pool serializes access
// to each language server behind its own lock, so parallel queries never
// conflict; an applied rename must not race other file edits.
func (p *BuiltinLSPPlugin) IsConcurrencySafe(args []string) bool { return !lspMutates(args) }

// DescribeCall surfaces what is being navigated so the spinner reads
// "LSP: references of cli/cli.go:128" instead of the raw JSON envelope.
func (p *BuiltinLSPPlugin) DescribeCall(args []string) string {
	cmd, inner, err := parseLSPInvocation(args)
	if err != nil {
		return i18n.T("plugins.lsp.describe_generic")
	}
	in := decodeLSPRaw(inner)
	if cmd == "workspace_symbol" {
		if in.Query == "" {
			return i18n.T("plugins.lsp.describe_generic")
		}
		return i18n.T("plugins.lsp.describe_workspace_symbol", describeTrim(in.Query))
	}
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
	case "implementation":
		return i18n.T("plugins.lsp.describe_implementation", target, in.Line)
	case "rename":
		if in.DryRun {
			return i18n.T("plugins.lsp.describe_rename_preview", target, in.Line, describeTrim(in.NewName))
		}
		return i18n.T("plugins.lsp.describe_rename", target, in.Line, describeTrim(in.NewName))
	default:
		return i18n.T("plugins.lsp.describe_generic")
	}
}
