/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import "github.com/diillson/chatcli/i18n"

// Capability advertisements for BuiltinMCPLoginPlugin.

// IsReadOnly reports true only for the "status" and "tools" subcommands.
// login/logout/restart have side effects (browser flow, token mint/delete,
// server process restart), so they
// stay gated: omitting a read-only claim for them makes the policy engine treat
// them as confirm-first actions.
func (p *BuiltinMCPLoginPlugin) IsReadOnly(args []string) bool {
	cmd, _, err := parseMCPLoginInvocation(args)
	return err == nil && (cmd == "status" || cmd == "tools")
}

// IsConcurrencySafe reports false: an interactive OAuth flow binds a fixed
// loopback port and reconnects a server — it must not run alongside another
// login or a reconnect of the same server.
func (p *BuiltinMCPLoginPlugin) IsConcurrencySafe(_ []string) bool { return false }

// DescribeCall surfaces the action so the spinner reads "MCP auth: login aws".
func (p *BuiltinMCPLoginPlugin) DescribeCall(args []string) string {
	cmd, server, err := parseMCPLoginInvocation(args)
	if err != nil {
		return i18n.T("plugins.mcplogin.describe_generic")
	}
	switch cmd {
	case "login":
		return i18n.T("plugins.mcplogin.describe_login", server)
	case "logout":
		return i18n.T("plugins.mcplogin.describe_logout", server)
	case "restart":
		return i18n.T("plugins.mcpsrv.describe_restart", server)
	case "tools":
		return i18n.T("plugins.mcpsrv.describe_tools")
	default:
		return i18n.T("plugins.mcplogin.describe_status")
	}
}
