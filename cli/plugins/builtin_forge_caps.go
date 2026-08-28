/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"github.com/diillson/chatcli/i18n"
)

// Capability advertisements for BuiltinForgePlugin, in the same per-plugin
// caps file shape as @lsp/@browser.

// IsReadOnly reports whether this invocation only reads forge state.
// Mutations (pr-create, pr-comment, issue-comment) go through the security
// gate; unparseable args fail closed.
func (p *BuiltinForgePlugin) IsReadOnly(args []string) bool {
	inv, err := parseForgeInvocation(args)
	if err != nil {
		return false
	}
	if inv.cmd == "" {
		return false
	}
	return !forgeMutatingCmds[inv.cmd]
}

// IsConcurrencySafe reports true for read invocations (independent CLI
// calls) and false for mutations, which must keep their relative order.
func (p *BuiltinForgePlugin) IsConcurrencySafe(args []string) bool {
	return p.IsReadOnly(args)
}

// DescribeCall surfaces the operation so the spinner reads
// "Forge: checks of PR 42" instead of the raw JSON envelope.
func (p *BuiltinForgePlugin) DescribeCall(args []string) string {
	inv, err := parseForgeInvocation(args)
	if err != nil {
		return i18n.T("plugins.forge.describe_generic")
	}
	switch inv.cmd {
	case "pr-list":
		return i18n.T("plugins.forge.describe_pr_list")
	case "pr-view", "pr-diff":
		return i18n.T("plugins.forge.describe_pr_view", inv.number)
	case "pr-checks":
		return i18n.T("plugins.forge.describe_pr_checks", inv.number)
	case "pr-create":
		return i18n.T("plugins.forge.describe_pr_create", describeTrim(inv.title))
	case "pr-comment", "issue-comment":
		return i18n.T("plugins.forge.describe_comment", inv.number)
	case "issue-list":
		return i18n.T("plugins.forge.describe_issue_list")
	case "issue-view":
		return i18n.T("plugins.forge.describe_issue_view", inv.number)
	case "ci-status":
		return i18n.T("plugins.forge.describe_ci_status")
	case "ci-logs":
		return i18n.T("plugins.forge.describe_ci_logs", inv.run)
	default:
		return i18n.T("plugins.forge.describe_generic")
	}
}
