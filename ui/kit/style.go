/*
 * ChatCLI - UI kit: role-based styling
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package kit

import (
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/diillson/chatcli/ui/theme"
	"github.com/mattn/go-runewidth"
)

func init() {
	// Emoji must measure as terminals render them (2 cells) or box borders
	// drift next to any emoji-bearing content. Identical to the pin in
	// cli/agent (idempotent while both exist; the agent copy retires when
	// its geometry moves here).
	runewidth.DefaultCondition.StrictEmojiNeutral = false
	runewidth.DefaultCondition.EastAsianWidth = false
}

const (
	sgrBold = "\033[1m"
)

// Style returns a lipgloss style whose foreground is the role's color under
// the active theme and profile — the lipgloss counterpart of Colorize,
// mirroring the palette overlay's style(role) pattern.
func Style(r theme.Role) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Lip(r))
}

// Colorize wraps s in the role's color span. On colorless profiles both the
// color and the reset are empty strings, so the output is exactly s.
func Colorize(s string, r theme.Role) string {
	c := theme.ANSI(r)
	if c == "" {
		return s
	}
	return c + s + theme.Reset()
}

// Bold renders s bold in the role's color — the "title weight" of the
// typographic hierarchy. No escapes are emitted on colorless profiles.
func Bold(s string, r theme.Role) string {
	if !theme.ActiveProfile().HasColor() {
		return s
	}
	return sgrBold + theme.ANSI(r) + s + theme.Reset()
}

// Dim renders s in the muted role — the "metadata weight" of the hierarchy.
func Dim(s string) string {
	return Colorize(s, theme.RoleMuted)
}

// VisibleLen measures the display width of s in terminal cells, ignoring
// ANSI escapes. This is the kit's single measurement path (lipgloss.Width),
// shared with the agent renderer so every component agrees on geometry.
func VisibleLen(s string) int {
	return lipgloss.Width(s)
}

// Truncate clamps s to at most maxCols visible columns, preserving ANSI
// color sequences and appending an ellipsis plus a reset when content was
// dropped so styling never bleeds into the next line.
func Truncate(s string, maxCols int) string {
	if VisibleLen(s) <= maxCols {
		return s
	}
	var b strings.Builder
	cols := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			if end := ansiSeqEnd(s[i:]); end > 0 {
				b.WriteString(s[i : i+end])
				i += end
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		w := runewidth.RuneWidth(r)
		if cols+w > maxCols-1 { // reserve one column for the ellipsis
			break
		}
		b.WriteRune(r)
		cols += w
		i += size
	}
	return b.String() + GlyphEllipsis.unicodeForm() + theme.Reset()
}

// ansiSeqEnd returns the byte length of the ANSI CSI sequence starting at
// s[0] (which must be ESC), or 0 when s is not a CSI sequence.
func ansiSeqEnd(s string) int {
	if len(s) < 2 || s[1] != '[' {
		return 0
	}
	for i := 2; i < len(s); i++ {
		c := s[i]
		if c >= 0x40 && c <= 0x7e { // final byte
			return i + 1
		}
	}
	return 0
}
