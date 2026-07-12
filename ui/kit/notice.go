/*
 * ChatCLI - UI kit: status notices
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package kit

import "github.com/diillson/chatcli/ui/theme"

// Level classifies a Notice.
type Level int

const (
	// LevelSuccess reports a completed action.
	LevelSuccess Level = iota
	// LevelError reports a failure.
	LevelError
	// LevelWarn reports a warning.
	LevelWarn
	// LevelInfo reports neutral information.
	LevelInfo
)

// glyphFor maps a level to its canonical glyph.
func (l Level) glyph() Glyph {
	switch l {
	case LevelError:
		return GlyphError
	case LevelWarn:
		return GlyphWarn
	case LevelInfo:
		return GlyphInfo
	default:
		return GlyphSuccess
	}
}

// Notice renders a one-line status message on the grid: two-space indent,
// role-colored glyph, message. Error and warning messages are tinted with
// the glyph's role so the whole line reads as one signal; success and info
// keep the message in default text (the glyph alone carries the state).
func Notice(l Level, msg string) string {
	g := l.glyph()
	body := msg
	if l == LevelError || l == LevelWarn {
		body = Colorize(msg, g.Role())
	}
	return "  " + Sym(g) + " " + body
}

// NoticeRole renders a notice-shaped line with an arbitrary glyph and role —
// the escape hatch for surfaces with domain-specific markers (e.g. the
// running spinner summary) that still want the grid alignment.
func NoticeRole(g Glyph, msg string, r theme.Role) string {
	return "  " + Colorize(g.String(), r) + " " + msg
}
