/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package coder

import (
	"encoding/json"
	"html"
	"strings"
)

// execValueFlags and execBoolFlags mirror the flag set of the engine's exec
// subcommand (pkg/coder/engine handleExec). Any other flag makes exec fail to
// parse, so it is treated as "cannot tell" here.
var (
	execValueFlags = map[string]bool{"cmd": true, "dir": true, "workingDir": true, "workdir": true, "cwd": true, "timeout": true}
	execBoolFlags  = map[string]bool{"allow-unsafe": true, "allow-sudo": true}
)

// execShellCommand returns the exact shell line `@coder exec` will run for
// these tool args, and false when that cannot be determined without doubt.
// The read-only auto-allow must classify exactly what exec runs, so every
// form the tool-call pipeline might rewrite before execution is refused:
// backslashes and line breaks (line continuations, JSON escapes, quote
// repairs), HTML entities (decoded once by the argument sanitizer and again
// by exec), and JSON whose sources for the command disagree.
func execShellCommand(args string) (string, bool) {
	trimmed := strings.TrimSpace(args)
	if trimmed == "" || strings.ContainsAny(trimmed, "\\\r\n") || html.UnescapeString(trimmed) != trimmed {
		return "", false
	}
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		return execCommandFromJSON(trimmed)
	}
	argv, ok := splitQuotedArgs(trimmed)
	if !ok {
		return "", false
	}
	return execCommandFromArgv(argv)
}

// execCommandFromArgv applies the exec flag set to argv (argv[0] is the
// subcommand) with Go flag semantics: parsing stops at the first non-flag,
// a value flag takes the next argument whatever it looks like, and the last
// occurrence wins.
func execCommandFromArgv(argv []string) (string, bool) {
	if len(argv) == 0 || argv[0] != "exec" {
		return "", false
	}
	cmd, found := "", false
	for i := 1; i < len(argv); i++ {
		tok := argv[i]
		if tok == "--" || tok == "-" || !strings.HasPrefix(tok, "-") {
			break
		}
		name := strings.TrimPrefix(strings.TrimPrefix(tok, "-"), "-")
		if name == "" || strings.HasPrefix(name, "-") || strings.HasPrefix(name, "=") {
			return "", false
		}
		name, value, hasValue := strings.Cut(name, "=")
		switch {
		case execBoolFlags[name]:
			continue
		case !execValueFlags[name]:
			return "", false
		}
		if !hasValue {
			if i+1 >= len(argv) {
				return "", false
			}
			i++
			value = argv[i]
		}
		if name == "cmd" {
			cmd, found = value, true
		}
	}
	if !found || cmd == "" {
		return "", false
	}
	return cmd, true
}

// execCommandFromJSON resolves the command from a JSON tool call. An argv
// array goes through the flag semantics; an object envelope must name the
// command in exactly one way: every place the argument builders read it
// from (args/flags "cmd" or "command", a top-level "command") has to agree.
func execCommandFromJSON(s string) (string, bool) {
	var payload any
	if err := json.Unmarshal([]byte(s), &payload); err != nil {
		return "", false
	}
	switch v := payload.(type) {
	case []any:
		argv, ok := stringSlice(v)
		if !ok {
			return "", false
		}
		return execCommandFromArgv(argv)
	case map[string]any:
		return execCommandFromEnvelope(v)
	}
	return "", false
}

func execCommandFromEnvelope(m map[string]any) (string, bool) {
	if sub, _ := m["cmd"].(string); sub != "exec" {
		return "", false
	}
	if _, ok := m["positional"]; ok {
		return "", false
	}
	if _, ok := m["_"]; ok {
		return "", false
	}
	if raw, ok := m["argv"]; ok {
		items, isList := raw.([]any)
		argv, ok := stringSlice(items)
		if !isList || !ok {
			return "", false
		}
		if len(argv) == 0 || argv[0] != "exec" {
			argv = append([]string{"exec"}, argv...)
		}
		return execCommandFromArgv(argv)
	}
	var candidates []any
	if v, ok := m["command"]; ok {
		candidates = append(candidates, v)
	}
	for _, envelope := range []string{"args", "flags"} {
		raw, ok := m[envelope]
		if !ok {
			continue
		}
		inner, isMap := raw.(map[string]any)
		if !isMap {
			return "", false
		}
		for _, key := range []string{"cmd", "command"} {
			if v, ok := inner[key]; ok {
				candidates = append(candidates, v)
			}
		}
	}
	if len(candidates) == 0 {
		return "", false
	}
	first, ok := candidates[0].(string)
	if !ok || first == "" {
		return "", false
	}
	for _, c := range candidates[1:] {
		if s, ok := c.(string); !ok || s != first {
			return "", false
		}
	}
	return first, true
}

// chainsCommands reports whether a shell line runs more than one command
// through a separator or a substitution (";", "&", "&&", "||", backticks,
// "$(" or a line break). IsReadOnlyCommand classifies the first command of
// the line, so a chained line is never auto-allowed on that basis. Pipes are
// left to IsReadOnlyCommand, which checks pipe targets itself.
func chainsCommands(cmdLine string) bool {
	return strings.ContainsAny(cmdLine, ";&`\r\n") ||
		strings.Contains(cmdLine, "||") ||
		strings.Contains(cmdLine, "$(")
}

func stringSlice(items []any) ([]string, bool) {
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// splitQuotedArgs splits a flat argument line the way the tool-call parser
// does for input without backslashes or line breaks: whitespace separates,
// single and double quotes group. Unbalanced quotes are refused.
func splitQuotedArgs(s string) ([]string, bool) {
	var out []string
	var buf strings.Builder
	inSingle, inDouble, inToken := false, false, false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch == '\'' && !inDouble:
			inSingle = !inSingle
			inToken = true
		case ch == '"' && !inSingle:
			inDouble = !inDouble
			inToken = true
		case (ch == ' ' || ch == '\t') && !inSingle && !inDouble:
			if buf.Len() > 0 {
				out = append(out, buf.String())
			}
			buf.Reset()
			inToken = false
		default:
			buf.WriteByte(ch)
			inToken = true
		}
	}
	if inSingle || inDouble {
		return nil, false
	}
	if inToken && buf.Len() > 0 {
		out = append(out, buf.String())
	}
	return out, true
}
