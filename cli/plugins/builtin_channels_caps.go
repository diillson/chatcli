/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"strings"

	"github.com/diillson/chatcli/i18n"
)

// Capability advertisements for BuiltinChannelsPlugin.

// IsReadOnly depends on the subcommand: list/unread never mutate anything;
// ack resets the inbox attention counters (benign, but state-changing), so
// only the read paths advertise read-only.
func (p *BuiltinChannelsPlugin) IsReadOnly(args []string) bool {
	inv := parseChannelsInvocation(strings.Join(args, " "))
	return canonicalChannelsCmd(inv.Cmd) != "ack"
}

// IsConcurrencySafe returns true: the channel ring is lock-protected and
// every subcommand is a single bounded operation.
func (p *BuiltinChannelsPlugin) IsConcurrencySafe(_ []string) bool { return true }

// DescribeCall renders the spinner label for the current invocation.
func (p *BuiltinChannelsPlugin) DescribeCall(args []string) string {
	inv := parseChannelsInvocation(strings.Join(args, " "))
	return i18n.T("plugins.channels.describe", canonicalChannelsCmd(inv.Cmd))
}
