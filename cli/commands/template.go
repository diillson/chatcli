/*
 * ChatCLI - Slash command template expansion
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Pure text interpolation — no eval, no reflection, provider-agnostic by
 * construction. Placeholders (Claude Code convention, so interop files work
 * unchanged):
 *
 *   $ARGUMENTS  — the raw argument string, verbatim
 *   $1 … $9     — whitespace-split positional arguments
 *   $$          — a literal '$'
 *
 * Lines whose first non-blank column is "!" are PRE-EXECUTION lines: the
 * rest of the line is a shell command whose output replaces the line in the
 * expanded prompt. Execution is delegated to the caller-supplied runner —
 * this package never touches os/exec, so the security gate (policy engine +
 * interactive approval) stays in exactly one place, owned by the CLI layer.
 */
package commands

import (
	"strings"
)

// ExecRunner runs one pre-execution shell line. ok=false means the command
// was denied (by policy or by the user) — the line is replaced by a denial
// marker so the model knows the output is missing rather than empty.
type ExecRunner func(command string) (output string, ok bool)

// Expand interpolates args into the command body and resolves pre-execution
// lines through runner. A nil runner disables pre-execution: the lines are
// replaced by the denial marker (fail-safe for surfaces with no gate).
func Expand(cmd *Command, args string, runner ExecRunner) string {
	body := interpolate(cmd.Content, args)
	return resolvePreExec(body, runner)
}

// PreExecLines returns the shell commands the template would run, after
// interpolation — surfaced to the user before any approval prompt so they
// approve what will actually execute, not the template text.
func PreExecLines(cmd *Command, args string) []string {
	var out []string
	inFence := false
	for _, line := range strings.Split(interpolate(cmd.Content, args), "\n") {
		if isFenceDelimiter(line) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if sh, ok := preExecCommand(line); ok {
			out = append(out, sh)
		}
	}
	return out
}

// isFenceDelimiter reports whether line opens or closes a fenced code
// block. Lines inside fences are NEVER pre-executed: a template may show a
// bash snippet whose lines start with "!" (history expansion, negation)
// without turning documentation into execution.
func isFenceDelimiter(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~")
}

// interpolate substitutes $ARGUMENTS, $1..$9 and $$ in a single pass.
// Unknown placeholders (e.g. "$COST") pass through verbatim — a template
// mentioning shell variables must not be mangled.
func interpolate(body, args string) string {
	args = strings.TrimSpace(args)
	fields := strings.Fields(args)

	var b strings.Builder
	b.Grow(len(body) + len(args))
	for i := 0; i < len(body); i++ {
		ch := body[i]
		if ch != '$' {
			b.WriteByte(ch)
			continue
		}
		rest := body[i+1:]
		switch {
		case strings.HasPrefix(rest, "$"):
			b.WriteByte('$')
			i++
		case strings.HasPrefix(rest, "ARGUMENTS"):
			b.WriteString(args)
			i += len("ARGUMENTS")
		case len(rest) > 0 && rest[0] >= '1' && rest[0] <= '9':
			idx := int(rest[0] - '1')
			if idx < len(fields) {
				b.WriteString(fields[idx])
			}
			i++
		default:
			b.WriteByte('$')
		}
	}
	return b.String()
}

// preExecCommand reports whether line is a pre-execution line ("!cmd" at
// the first non-blank column) and returns the shell command.
func preExecCommand(line string) (string, bool) {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "!") {
		return "", false
	}
	sh := strings.TrimSpace(strings.TrimPrefix(t, "!"))
	if sh == "" {
		return "", false
	}
	return sh, true
}

// deniedMarker replaces a pre-exec line whose command was denied or whose
// surface has no runner. Explicit on purpose: the model must know the
// output is MISSING, not empty — silence would read as "command produced
// nothing".
const deniedMarker = "[pre-execution command not run: denied by the security gate]"

// resolvePreExec replaces each pre-exec line with its command's output,
// skipping fenced code blocks (see isFenceDelimiter).
func resolvePreExec(body string, runner ExecRunner) string {
	if !strings.Contains(body, "!") {
		return body
	}
	lines := strings.Split(body, "\n")
	out := make([]string, 0, len(lines))
	inFence := false
	for _, line := range lines {
		if isFenceDelimiter(line) {
			inFence = !inFence
			out = append(out, line)
			continue
		}
		sh, isExec := preExecCommand(line)
		if inFence || !isExec {
			out = append(out, line)
			continue
		}
		if runner == nil {
			out = append(out, deniedMarker)
			continue
		}
		result, ok := runner(sh)
		if !ok {
			out = append(out, deniedMarker)
			continue
		}
		out = append(out, "Output of `"+sh+"`:\n```\n"+strings.TrimRight(result, "\n")+"\n```")
	}
	return strings.Join(out, "\n")
}
