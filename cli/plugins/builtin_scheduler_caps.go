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

// Fase 2.1 capability advertisements for BuiltinSchedulerPlugin, in a
// separate file from builtin_scheduler.go so the larger original is
// not dragged into the Quality Gate's cyclo-new scan.

// IsReadOnly reports true for query/list operations; schedule/wait/
// cancel mutate the scheduler's persistent state. We look at the
// first arg (subcommand) which is the stable @scheduler schema entry
// point.
func (p *BuiltinSchedulerPlugin) IsReadOnly(args []string) bool {
	sub := schedulerSubcommand(args)
	return sub == "query" || sub == "list"
}

// IsConcurrencySafe matches IsReadOnly: two queries or two lists can
// run in parallel against the durable store without conflict (the
// store uses a per-job lock). Mutators stay serial to preserve causal
// ordering of the schedule/cancel chain.
func (p *BuiltinSchedulerPlugin) IsConcurrencySafe(args []string) bool {
	return p.IsReadOnly(args)
}

// DescribeCall surfaces the subcommand and, when available, the job
// identifier or schedule name being acted on. All strings are
// i18n-resolved.
func (p *BuiltinSchedulerPlugin) DescribeCall(args []string) string {
	sub := schedulerSubcommand(args)
	switch sub {
	case "schedule":
		if n := extractStringArg(args, "name"); n != "" {
			return i18n.T("plugins.scheduler.describe.schedule", n)
		}
	case "query":
		if id := extractStringArg(args, "id"); id != "" {
			return i18n.T("plugins.scheduler.describe.query", id)
		}
	case "cancel":
		if id := extractStringArg(args, "id"); id != "" {
			return i18n.T("plugins.scheduler.describe.cancel", id)
		}
	case "wait":
		if u := extractStringArg(args, "until"); u != "" {
			return i18n.T("plugins.scheduler.describe.wait", u)
		}
	case "list":
		return i18n.T("plugins.scheduler.describe.list")
	}
	if sub != "" {
		return i18n.T("plugins.scheduler.describe.generic", sub)
	}
	return p.Description()
}

// schedulerSubcommand pulls the subcommand from either the JSON
// envelope or the positional argv form, matching the parser logic in
// parseSchedulerInvocation.
func schedulerSubcommand(args []string) string {
	if len(args) == 0 {
		return ""
	}
	first := strings.TrimSpace(args[0])
	if strings.HasPrefix(first, "{") {
		if v := extractStringArg([]string{first}, "cmd", "command"); v != "" {
			return v
		}
	}
	return first
}
