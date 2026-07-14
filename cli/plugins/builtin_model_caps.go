/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"github.com/diillson/chatcli/i18n"
)

// Capability advertisements for BuiltinModelPlugin.

// IsReadOnly depends on the subcommand: list/status only observe; use/reset
// mutate the agent loop's routing, and delegate spends tokens on another
// model.
func (p *BuiltinModelPlugin) IsReadOnly(args []string) bool {
	inv, err := parseModelInvocation(args)
	if err != nil {
		return false
	}
	return inv.cmd == "list" || inv.cmd == "status"
}

// IsConcurrencySafe: list/status are safe in parallel batches; delegate is an
// independent one-shot with no shared state, so it is safe too. use/reset
// mutate the shared route override and MUST run serially.
func (p *BuiltinModelPlugin) IsConcurrencySafe(args []string) bool {
	inv, err := parseModelInvocation(args)
	if err != nil {
		return false
	}
	return inv.cmd == "list" || inv.cmd == "status" || inv.cmd == "delegate"
}

// DescribeCall labels the spinner with the subcommand and target model.
func (p *BuiltinModelPlugin) DescribeCall(args []string) string {
	inv, err := parseModelInvocation(args)
	if err != nil {
		return p.Description()
	}
	switch inv.cmd {
	case "use":
		return i18n.T("plugins.model.describe.use", inv.model)
	case "delegate":
		return i18n.T("plugins.model.describe.delegate", inv.model)
	case "reset":
		return i18n.T("plugins.model.describe.reset")
	case "list":
		return i18n.T("plugins.model.describe.list")
	default:
		return i18n.T("plugins.model.describe.status")
	}
}
