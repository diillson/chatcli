/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"github.com/diillson/chatcli/i18n"
)

// Capability advertisements for BuiltinTaskGraphPlugin.

// taskGraphReadOnlyCmds are the subcommands that only inspect persisted
// state. Everything else (plan/run/retry/cancel) mutates runs or spawns
// workers, so the default is fail-closed.
var taskGraphReadOnlyCmds = map[string]bool{
	"status": true, "state": true, "progress": true,
	"show": true, "task": true, "inspect": true, "get": true,
	"list": true, "ls": true, "runs": true,
}

// IsReadOnly re-parses the invocation and answers per subcommand; a parse
// failure reports false (fail closed).
func (p *BuiltinTaskGraphPlugin) IsReadOnly(args []string) bool {
	sub, _, err := parseTaskGraphInvocation(args)
	if err != nil {
		return false
	}
	return taskGraphReadOnlyCmds[sub]
}

// IsConcurrencySafe mirrors IsReadOnly: inspections may run inside a
// parallel batch; a run/retry monopolizes the dispatcher and must not.
func (p *BuiltinTaskGraphPlugin) IsConcurrencySafe(args []string) bool {
	return p.IsReadOnly(args)
}

// DescribeCall surfaces the subcommand on the spinner line.
func (p *BuiltinTaskGraphPlugin) DescribeCall(args []string) string {
	sub, payload, err := parseTaskGraphInvocation(args)
	if err != nil {
		return i18n.T("plugins.taskgraph.describe_generic")
	}
	switch sub {
	case "plan", "create", "validate":
		return i18n.T("plugins.taskgraph.describe_plan")
	case "run", "start", "resume", "exec":
		return i18n.T("plugins.taskgraph.describe_run")
	case "retry", "reopen":
		return i18n.T("plugins.taskgraph.describe_retry", describeTrim(jsonString(payload, "task", "task_id", "taskId")))
	case "cancel", "stop", "abort":
		return i18n.T("plugins.taskgraph.describe_cancel")
	case "prune", "gc", "clean":
		return i18n.T("plugins.taskgraph.describe_prune")
	case "dash", "dashboard", "ui":
		return i18n.T("plugins.taskgraph.describe_dash")
	default:
		return i18n.T("plugins.taskgraph.describe_status")
	}
}
