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

// Capability advertisements for BuiltinProcPlugin.

// IsReadOnly is PER-SUBCOMMAND: status/logs/list only observe; start launches
// a process and stop/remove mutate the process table — those go through the
// orchestrator's confirmation policy like any other mutating tool call.
func (p *BuiltinProcPlugin) IsReadOnly(args []string) bool {
	switch procInvocationCmd(args) {
	case "status", "logs", "list", "wait":
		return true
	default:
		return false
	}
}

// IsConcurrencySafe mirrors IsReadOnly: status, logs and list are
// independent reads of the process table; start, stdin, stop and remove
// change it (and their order matters — stop before start of the same
// port, stdin after start), so they stay serial.
func (p *BuiltinProcPlugin) IsConcurrencySafe(args []string) bool { return p.IsReadOnly(args) }

// DescribeCall surfaces what is being supervised in the spinner.
func (p *BuiltinProcPlugin) DescribeCall(args []string) string {
	cmd, inner, err := parseProcInvocation(args)
	if err != nil {
		return i18n.T("plugins.proc.describe_generic")
	}
	var in struct {
		Command string `json:"command"`
		ID      string `json:"id"`
	}
	_ = json.Unmarshal([]byte(inner), &in)
	switch cmd {
	case "start":
		return i18n.T("plugins.proc.describe_start", describeTrim(in.Command))
	case "stdin":
		return i18n.T("plugins.proc.describe_stdin", in.ID)
	case "status":
		return i18n.T("plugins.proc.describe_status", in.ID)
	case "logs":
		return i18n.T("plugins.proc.describe_logs", in.ID)
	case "wait":
		return i18n.T("plugins.proc.describe_wait", in.ID)
	case "stop":
		return i18n.T("plugins.proc.describe_stop", in.ID)
	case "remove":
		return i18n.T("plugins.proc.describe_remove", in.ID)
	case "list":
		return i18n.T("plugins.proc.describe_list")
	default:
		return i18n.T("plugins.proc.describe_generic")
	}
}
