/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"strings"

	"github.com/diillson/chatcli/cli/agent/ask"
	"github.com/diillson/chatcli/i18n"
)

// Capability advertisements for BuiltinAskPlugin.

// IsReadOnly returns true: @ask never touches the filesystem or external state;
// it only collects a decision from the user.
func (p *BuiltinAskPlugin) IsReadOnly(_ []string) bool { return true }

// IsConcurrencySafe returns false: @ask takes over the terminal with a Bubble
// Tea overlay, so it MUST run serially — two overlays at once would fight for
// the TTY.
func (p *BuiltinAskPlugin) IsConcurrencySafe(_ []string) bool { return false }

// DescribeCall reports how many questions are being asked, for the spinner
// label.
func (p *BuiltinAskPlugin) DescribeCall(args []string) string {
	payload := strings.TrimSpace(strings.Join(args, " "))
	if qs, err := ask.ParseRequest(payload); err == nil {
		return i18n.T("plugins.ask.describe", len(qs))
	}
	return p.Description()
}
