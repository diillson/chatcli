/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"github.com/diillson/chatcli/i18n"
)

// Capability advertisements for BuiltinDashPlugin.

// IsReadOnly depends on the subcommand: status, url, summary and events
// only observe (url may start the server, which is local, read-only state
// of this session); open opens a browser, off stops the server and mark
// writes to the timeline.
func (p *BuiltinDashPlugin) IsReadOnly(args []string) bool {
	inv, err := parseDashInvocation(args)
	if err != nil {
		return false
	}
	switch inv.cmd {
	case "status", "url", "summary", "events":
		return true
	}
	return false
}

// IsConcurrencySafe mirrors IsReadOnly: observation runs alongside anything,
// while open/off/mark touch the session's one dashboard and run serially.
func (p *BuiltinDashPlugin) IsConcurrencySafe(args []string) bool {
	return p.IsReadOnly(args)
}

// DescribeCall labels the spinner with the subcommand.
func (p *BuiltinDashPlugin) DescribeCall(args []string) string {
	inv, err := parseDashInvocation(args)
	if err != nil {
		return p.Description()
	}
	switch inv.cmd {
	case "open", "url":
		return i18n.T("plugins.dash.describe.open")
	case "off":
		return i18n.T("plugins.dash.describe.off")
	case "summary":
		return i18n.T("plugins.dash.describe.summary")
	case "events":
		return i18n.T("plugins.dash.describe.events")
	case "mark":
		return i18n.T("plugins.dash.describe.mark")
	default:
		return i18n.T("plugins.dash.describe.status")
	}
}
