/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"github.com/diillson/chatcli/i18n"
)

// contextSubcommand peeks at the invocation to classify the call without the
// full parse pipeline (used by the capability methods below).
func contextSubcommand(args []string) (cmd, name string) {
	c, raw, err := parseContextInvocation(args)
	if err != nil {
		return "", ""
	}
	return c, jsonString(raw, "name")
}

// IsReadOnly reports true only for the inspecting subcommands. create/attach/
// detach/delete mutate the context store or the session, so they are NOT
// read-only and (in coder mode) go through the policy confirmation.
func (*BuiltinContextPlugin) IsReadOnly(args []string) bool {
	switch cmd, _ := contextSubcommand(args); cmd {
	case "list", "status":
		return true
	default:
		return false
	}
}

// IsConcurrencySafe mirrors IsReadOnly: only the read-only inspections can run
// in parallel with other tools. Mutations are serialized.
func (p *BuiltinContextPlugin) IsConcurrencySafe(args []string) bool {
	return p.IsReadOnly(args)
}

// DescribeCall surfaces a contextual one-liner for the spinner.
func (*BuiltinContextPlugin) DescribeCall(args []string) string {
	cmd, name := contextSubcommand(args)
	switch cmd {
	case "create":
		return i18n.T("plugins.context.describe.create", name)
	case "attach":
		return i18n.T("plugins.context.describe.attach", name)
	case "detach":
		return i18n.T("plugins.context.describe.detach", name)
	case "list":
		return i18n.T("plugins.context.describe.list")
	case "status":
		return i18n.T("plugins.context.describe.status")
	case "delete":
		return i18n.T("plugins.context.describe.delete", name)
	default:
		return i18n.T("plugins.context.description")
	}
}
