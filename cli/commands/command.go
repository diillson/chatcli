/*
 * ChatCLI - Slash command catalog (types)
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * A slash command is a reusable, parameterized PROMPT TEMPLATE stored as a
 * markdown file and invoked as "/<name> [args]". It differs from a skill on
 * purpose: a skill is knowledge injected as system-prompt guidance ("how we
 * do X here"), sticky and subject to aging; a command is an action ("do X
 * now with these args") whose body BECOMES the user turn, consumed once.
 *
 * Because expansion happens before the request is built, commands are
 * provider-agnostic by construction — no native tool calling, no
 * provider-specific API surface — and work identically on every surface
 * (REPL chat/coder, one-shot -p, gateway, ACP, MCP, scheduler).
 */
package commands

import (
	"strings"
)

// Source identifies which directory family a command was loaded from. Kept
// as human-readable strings: they surface verbatim in /config commands.
type Source string

const (
	// SourceProject is <project>/.chatcli/commands — versioned with the
	// repo, shared with the team. Highest precedence.
	SourceProject Source = "project"
	// SourceGlobal is ~/.chatcli/commands — the user's personal commands.
	SourceGlobal Source = "global"
	// SourceClaude is <project>/.claude/commands — Claude Code interop:
	// teams that already keep commands there get them in ChatCLI with zero
	// migration.
	SourceClaude Source = "claude-interop"
	// SourceDevin is <project>/.devin/workflows — Devin workflow interop.
	SourceDevin Source = "devin-interop"
)

// Command is one loaded slash command.
type Command struct {
	// Name is the bare invocation token without slash or namespace
	// ("review-pr"). Derived from the filename, overridable by frontmatter.
	Name string
	// Namespace comes from the subdirectory ("frontend/deploy.md" →
	// namespace "frontend", invoked as /frontend:deploy). Empty for
	// top-level files.
	Namespace string
	// Description is shown in completers, palettes, ACP availableCommands
	// and MCP prompt listings.
	Description string
	// ArgumentHint documents the expected arguments ("<pr-number> [focus]").
	ArgumentHint string
	// Model / Effort optionally route the expanded turn, reusing the same
	// cross-provider hint plumbing manual skills use.
	Model  string
	Effort string
	// AllowedTools restricts which tools the model may call during the run
	// this command initiates (agent/coder surfaces). Enforced as an
	// ephemeral overlay on the security-gate check: a tool outside the
	// list escalates to an interactive ask, it is never silently allowed.
	AllowedTools []string
	// Content is the template body (frontmatter stripped).
	Content string
	// Path is the absolute file path (diagnostics + /config listing).
	Path string
	// Source records which directory family won for this name.
	Source Source
}

// InvocationName is the full user-facing token: "name" or "ns:name".
func (c *Command) InvocationName() string {
	if c.Namespace != "" {
		return c.Namespace + ":" + c.Name
	}
	return c.Name
}

// frontmatter is the YAML header of a command file. Field names follow the
// Claude Code convention so interop files parse unchanged.
type frontmatter struct {
	Name         string `yaml:"name"`
	Description  string `yaml:"description"`
	ArgumentHint string `yaml:"argument-hint"`
	Model        string `yaml:"model"`
	Effort       string `yaml:"effort"`
	AllowedTools string `yaml:"allowed-tools"`
}

// splitAllowedTools parses the comma-separated allowed-tools list.
func splitAllowedTools(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
