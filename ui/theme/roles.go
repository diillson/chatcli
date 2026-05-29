/*
 * ChatCLI - Semantic color roles
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * A Role is a USE of color, not a hue. Call sites ask the theme for "the
 * reasoning color" or "the tool-error color" — never for "cyan" or "red" —
 * so swapping the active theme re-skins every surface without editing a
 * single call site. This is the contract that lets the ~100 historical
 * `agent.ColorCyan`-style call sites migrate to a single semantic vocabulary.
 */
package theme

import "github.com/charmbracelet/lipgloss"

// Role enumerates every semantic color use in the UI. The zero value is a
// safe neutral (RoleBorder) so an unset role never renders an invisible or
// jarring color.
type Role int

const (
	// RoleBorder: default card/box border (the chat envelope border).
	RoleBorder Role = iota
	// RoleModelName: the active model name in the chat header.
	RoleModelName
	// RoleReasoning: the agent's thinking / plan cards.
	RoleReasoning
	// RoleExplanation: explanatory notes the agent emits between actions.
	RoleExplanation
	// RoleResponse: the assistant's final answer card.
	RoleResponse
	// RoleMultiAgent: multi-agent dispatch and batch headers.
	RoleMultiAgent
	// RoleAction: a tool invocation / action card.
	RoleAction
	// RoleToolSuccess: a tool that completed successfully.
	RoleToolSuccess
	// RoleToolError: a tool that failed.
	RoleToolError
	// RoleStatus: task progress / status lines.
	RoleStatus
	// RoleHeader: section headers and titled top borders.
	RoleHeader
	// RoleMuted: secondary text, metrics, "(default)" hints.
	RoleMuted
)

// ColorFor resolves a role to its palette color under the active theme.
func (t Theme) ColorFor(r Role) Color {
	p := t.Palette
	switch r {
	case RoleModelName:
		return p.Primary
	case RoleReasoning:
		return p.Accent
	case RoleExplanation:
		return p.Info
	case RoleResponse:
		return p.Muted
	case RoleMultiAgent:
		return p.Secondary
	case RoleAction:
		return p.Primary
	case RoleToolSuccess:
		return p.Success
	case RoleToolError:
		return p.Danger
	case RoleStatus:
		return p.Info
	case RoleHeader:
		return p.Accent
	case RoleMuted:
		return p.Muted
	default: // RoleBorder
		return p.Border
	}
}

// ANSIFor returns the foreground escape sequence for a role under the given
// profile. This is the primary call-site API: theme.Active().ANSIFor(role, p).
func (t Theme) ANSIFor(r Role, p Profile) string {
	return t.ColorFor(r).SGR(p)
}

// LipFor returns a lipgloss.Color for a role under the given profile — used
// by the box renderer for borders.
func (t Theme) LipFor(r Role, p Profile) lipgloss.Color {
	return t.ColorFor(r).Lip(p)
}
