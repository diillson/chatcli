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

// Fase 2.1 capability advertisements for BuiltinCoderPlugin, kept in a
// separate file from builtin_coder.go to keep the cyclo-new scan
// clean.

// IsReadOnly reports whether the @coder subcommand is read-only for
// the given args. Only `read`, `search`, `tree`, `list`, `stat` are
// pure reads — every other subcommand (`exec`, `write`, `patch`,
// `test`) mutates state.
func (p *BuiltinCoderPlugin) IsReadOnly(args []string) bool {
	sub := coderSubcommand(args)
	switch sub {
	case "read", "search", "tree", "list", "stat":
		return true
	}
	return false
}

// IsConcurrencySafe mirrors IsReadOnly: read/search/tree on
// independent paths can run in parallel; mutating subcommands stay
// serial so an exec and a write in the same batch never race.
func (p *BuiltinCoderPlugin) IsConcurrencySafe(args []string) bool {
	return p.IsReadOnly(args)
}

// DescribeCall surfaces what @coder is about to do: which subcommand,
// against which target. Strings are i18n-resolved at call time.
func (p *BuiltinCoderPlugin) DescribeCall(args []string) string {
	sub := coderSubcommand(args)
	switch sub {
	case "read":
		if f := extractPathArg(args); f != "" {
			return i18n.T("plugins.coder.describe.read", f)
		}
	case "search":
		if t := extractStringArg(args, "term", "pattern", "query"); t != "" {
			return i18n.T("plugins.coder.describe.search", t)
		}
	case "exec":
		if c := extractNestedArg(args, "cmd", "command"); c != "" {
			if len(c) > 60 {
				c = c[:60] + "..."
			}
			return i18n.T("plugins.coder.describe.exec", c)
		}
	case "write":
		if f := extractPathArg(args); f != "" {
			return i18n.T("plugins.coder.describe.write", f)
		}
	case "patch":
		if f := extractPathArg(args); f != "" {
			return i18n.T("plugins.coder.describe.patch", f)
		}
	case "tree":
		if d := extractStringArg(args, "dir", "path"); d != "" {
			return i18n.T("plugins.coder.describe.tree", d)
		}
	}
	if sub != "" {
		return i18n.T("plugins.coder.describe.generic", sub)
	}
	return p.Description()
}

// coderSubcommand extracts the @coder subcommand from the first arg,
// handling both the JSON envelope ({"cmd":"read","args":{…}}) and the
// positional form (`read --file foo`).
func coderSubcommand(args []string) string {
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
