/*
 * ChatCLI - Slash command execution mode
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * A command body is a prompt template, but WHERE that prompt should run is
 * not always the surface the user typed it on: a command whose body drives
 * tools is useless as a plain chat turn (chat mode is tool-less by design
 * and the model refuses). The execution mode captures the author's intent
 * so the dispatcher can route the invocation to the right engine.
 */
package commands

// ExecutionMode says which surface a command's expanded body targets.
type ExecutionMode string

const (
	// ExecModeChat runs the expanded body as a plain conversational turn.
	ExecModeChat ExecutionMode = "chat"
	// ExecModeCoder runs the expanded body through the coder ReAct loop
	// (one-shot when triggered from chat: execute, then return).
	ExecModeCoder ExecutionMode = "coder"
)

// ParseExecutionMode maps a raw frontmatter value to a mode. Tolerant by
// the same contract as the rest of the frontmatter: an unknown or empty
// value yields "" (no opinion) so resolution falls back to inference —
// a bad `mode:` never invalidates the command file.
func ParseExecutionMode(raw string) ExecutionMode {
	switch normalizeName(raw) {
	case "coder", "agent": // /coder and /agent share the ReAct engine
		return ExecModeCoder
	case "chat":
		return ExecModeChat
	default:
		return ""
	}
}

// ResolvedMode decides where this command wants to run. An explicit
// `mode:` always wins; without one, declaring allowed-tools is taken as
// intent to run with tools (interop files from Claude Code and friends
// carry allowed-tools but have no mode key). Everything else stays chat.
func (c *Command) ResolvedMode() ExecutionMode {
	if m := ParseExecutionMode(c.Mode); m != "" {
		return m
	}
	if len(c.AllowedTools) > 0 {
		return ExecModeCoder
	}
	return ExecModeChat
}
