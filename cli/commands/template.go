/*
 * ChatCLI - Slash command template expansion
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Pure text interpolation — no eval, no reflection, provider-agnostic by
 * construction. Placeholders cover every interop dialect so foreign files
 * work unchanged:
 *
 *   $ARGUMENTS  — the raw argument string, verbatim (Claude Code, Codex,
 *                 opencode)
 *   {{args}}    — Gemini CLI / Qwen Code alias for the same
 *   $1 … $9     — positional arguments; KEY=value tokens are excluded
 *   $KEY        — Codex named arguments, passed as KEY=value / KEY="v v"
 *   $$          — a literal '$'
 *
 * Execution surfaces, all delegated to the caller-supplied gated runner
 * (this package never touches os/exec):
 *
 *   ! cmd       — whole line replaced by the command's output
 *   !{cmd}      — Gemini inline form, substituted in place
 *   !`cmd`      — opencode inline form, substituted in place
 *
 * Fenced code blocks are never executed, on any of the three forms.
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

// PreExecLines returns the shell commands the template would run — whole
// "!" lines plus every inline !{…}/!`…` occurrence — after interpolation.
// Surfaced to the user before any approval prompt so they approve what will
// actually execute, not the template text.
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
			continue
		}
		out = append(out, inlineExecCommands(line)...)
	}
	return out
}

// inlineExecCommands extracts the inline execution occurrences of a line:
// Gemini's !{command} and opencode's !`command`. Returned in order of
// appearance.
func inlineExecCommands(line string) []string {
	var out []string
	for i := 0; i < len(line)-1; i++ {
		if line[i] != '!' {
			continue
		}
		var closer byte
		switch line[i+1] {
		case '{':
			closer = '}'
		case '`':
			closer = '`'
		default:
			continue
		}
		end := strings.IndexByte(line[i+2:], closer)
		if end < 0 {
			continue
		}
		if sh := strings.TrimSpace(line[i+2 : i+2+end]); sh != "" {
			out = append(out, sh)
		}
		i += 2 + end
	}
	return out
}

// resolveInlineExec substitutes each inline !{…}/!`…` occurrence with its
// gated output (single line, so the surrounding sentence stays readable) or
// the denial marker.
func resolveInlineExec(line string, runner ExecRunner) string {
	if !strings.Contains(line, "!") {
		return line
	}
	var b strings.Builder
	for i := 0; i < len(line); i++ {
		if line[i] != '!' || i+1 >= len(line) || (line[i+1] != '{' && line[i+1] != '`') {
			b.WriteByte(line[i])
			continue
		}
		closer := byte('}')
		if line[i+1] == '`' {
			closer = '`'
		}
		end := strings.IndexByte(line[i+2:], closer)
		if end < 0 {
			b.WriteByte(line[i])
			continue
		}
		sh := strings.TrimSpace(line[i+2 : i+2+end])
		if sh == "" {
			i += 2 + end
			continue
		}
		if runner == nil {
			b.WriteString(deniedMarker)
		} else if result, ok := runner(sh); ok {
			b.WriteString(strings.TrimSpace(result))
		} else {
			b.WriteString(deniedMarker)
		}
		i += 2 + end
	}
	return b.String()
}

// isFenceDelimiter reports whether line opens or closes a fenced code
// block. Lines inside fences are NEVER pre-executed: a template may show a
// bash snippet whose lines start with "!" (history expansion, negation)
// without turning documentation into execution.
func isFenceDelimiter(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~")
}

// interpolate substitutes the template placeholders in a single pass:
//
//	$ARGUMENTS   — the raw argument string, verbatim
//	{{args}}     — Gemini/Qwen alias for $ARGUMENTS
//	$1 … $9      — positional arguments (KEY=value tokens excluded)
//	$KEY         — Codex-style named argument, passed as KEY=value or
//	               KEY="quoted value" on invocation
//	$$           — a literal '$'
//
// Unknown placeholders (e.g. "$COST", "$UNSET_KEY") pass through verbatim —
// a template mentioning shell variables must not be mangled.
func interpolate(body, args string) string {
	args = strings.TrimSpace(args)
	positional, named := splitInvocationArgs(args)

	body = strings.ReplaceAll(body, "{{args}}", args)

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
			if idx < len(positional) {
				b.WriteString(positional[idx])
			}
			i++
		default:
			if key := leadingNamedKey(rest); key != "" {
				if val, ok := named[key]; ok {
					b.WriteString(val)
					i += len(key)
					continue
				}
			}
			b.WriteByte('$')
		}
	}
	return b.String()
}

// leadingNamedKey extracts an uppercase identifier ([A-Z][A-Z0-9_]*) at the
// start of s — the Codex named-placeholder shape.
func leadingNamedKey(s string) string {
	n := 0
	for n < len(s) {
		ch := s[n]
		if ch >= 'A' && ch <= 'Z' || ch == '_' || (n > 0 && ch >= '0' && ch <= '9') {
			n++
			continue
		}
		break
	}
	if n == 0 {
		return ""
	}
	return s[:n]
}

// splitInvocationArgs tokenizes the argument string: KEY=value and
// KEY="quoted value" tokens become named arguments (Codex convention);
// everything else stays positional, in order. $ARGUMENTS always carries the
// raw string, so no information is lost either way.
func splitInvocationArgs(args string) (positional []string, named map[string]string) {
	named = map[string]string{}
	i := 0
	for i < len(args) {
		for i < len(args) && args[i] == ' ' {
			i++
		}
		if i >= len(args) {
			break
		}
		key := ""
		// KEY= prefix?
		if k := leadingNamedKey(args[i:]); k != "" && i+len(k) < len(args) && args[i+len(k)] == '=' {
			key = k
			i += len(k) + 1
		}
		var val string
		if i < len(args) && (args[i] == '"' || args[i] == '\'') {
			quote := args[i]
			i++
			vStart := i
			for i < len(args) && args[i] != quote {
				i++
			}
			val = args[vStart:i]
			if i < len(args) {
				i++ // closing quote
			}
		} else {
			vStart := i
			for i < len(args) && args[i] != ' ' {
				i++
			}
			val = args[vStart:i]
		}
		if key != "" {
			named[key] = val
		} else {
			positional = append(positional, val)
		}
	}
	return positional, named
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
	// "!{...}" and "!`...`" are the INLINE syntaxes (Gemini / opencode) —
	// even at column zero they resolve in place, never as a whole-line
	// command (which would try to execute the literal braces).
	if sh[0] == '{' || sh[0] == '`' {
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
			if !inFence {
				line = resolveInlineExec(line, runner)
			}
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
