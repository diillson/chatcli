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

// Fase 2.1 capability advertisements for BuiltinParkPlugin. Park is a
// state-mutating primitive that suspends the agent loop, so its flags
// reflect that: never read-only, never concurrency-safe.

// IsReadOnly returns false: @park mutates scheduler state (it parks
// the agent and registers an auto-resume callback). Even though the
// observation subcommands (for_cmd, for_url) are mostly read-only at
// the user level, they still create durable scheduler entries.
func (p *BuiltinParkPlugin) IsReadOnly(_ []string) bool { return false }

// IsConcurrencySafe returns false: parking is a process-wide event
// that suspends the agent loop. It cannot meaningfully run in
// parallel with another tool — it IS the way the agent surrenders the
// turn.
func (p *BuiltinParkPlugin) IsConcurrencySafe(_ []string) bool { return false }

// DescribeCall reports which park flavor is being requested. The cmd
// vocabulary is delay / until / for_url / for_cmd; each gets its own
// human-readable prefix via i18n.
func (p *BuiltinParkPlugin) DescribeCall(args []string) string {
	if len(args) == 0 {
		return p.Description()
	}
	first := strings.TrimSpace(args[0])
	sub := first
	if strings.HasPrefix(first, "{") {
		if v := extractStringArg([]string{first}, "cmd"); v != "" {
			sub = v
		}
	}
	switch sub {
	case "delay":
		if d := extractStringArg(args, "duration"); d != "" {
			return i18n.T("plugins.park.describe.delay", d)
		}
	case "until":
		if t := extractStringArg(args, "deadline", "when"); t != "" {
			return i18n.T("plugins.park.describe.until", t)
		}
	case "for_url":
		if u := extractStringArg(args, "url"); u != "" {
			if len(u) > 60 {
				u = u[:60] + "..."
			}
			return i18n.T("plugins.park.describe.for_url", u)
		}
	case "for_cmd":
		if c := extractStringArg(args, "cmd", "command"); c != "" {
			if len(c) > 60 {
				c = c[:60] + "..."
			}
			return i18n.T("plugins.park.describe.for_cmd", c)
		}
	}
	return i18n.T("plugins.park.describe.generic", sub)
}
