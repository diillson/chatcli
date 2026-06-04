/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"strings"

	"github.com/diillson/chatcli/i18n"
)

// spinnerMessage turns a tool box label into a spinner message: the animation
// appends "... <glyph>", so trailing dots/ellipsis are trimmed to avoid
// "Mixture-of-Agents…...". Falls back to a generic verb when empty.
func spinnerMessage(label string) string {
	s := strings.TrimRight(strings.TrimSpace(label), ".… ")
	if s == "" {
		return i18n.T("agent.spinner.working")
	}
	return s
}

// defaultSpinnerLabel produces the legacy spinner label used before
// Item 7. The agent loop falls back to it when the plugin doesn't
// ship DescriberWithInput (legacy / external plugins) or when
// DescribeCall returns an empty string for some unusual args shape.
//
// Both the verb and the placeholder for an unknown subcommand are
// i18n-resolved so the user's locale wins. Kept as a standalone
// helper so the spinner-label decision logic is unit-testable
// without spinning up an AgentMode.
func defaultSpinnerLabel(toolName string, args []string) string {
	subCmd := i18n.T("agent.spinner.action_placeholder")
	if len(args) > 0 {
		subCmd = args[0]
	}
	return i18n.T("agent.spinner.executing", toolName, subCmd)
}
