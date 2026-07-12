/*
 * ChatCLI - UI kit: canonical status glyphs
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * One glyph per meaning, defined once. The audit found three success marks,
 * four error marks and eight bullet variants in the wild; this file is the
 * whole vocabulary from now on. Every glyph is deliberately chosen WITHOUT
 * the Unicode Emoji property, so runewidth (StrictEmojiNeutral=false)
 * measures them exactly as terminals render them — 1 cell — keeping columns
 * and box borders aligned on every platform, including Windows. Colored
 * emoji remains legal only as a box Icon or in the welcome logo; the U+FE0F
 * variation selector is banned everywhere (it silently flips a glyph from 1
 * to 2 cells).
 */
package kit

import (
	"strings"

	"github.com/diillson/chatcli/ui/theme"
)

// Glyph identifies one entry of the canonical status vocabulary.
type Glyph int

const (
	// GlyphSuccess marks a completed action.
	GlyphSuccess Glyph = iota
	// GlyphError marks a failed action.
	GlyphError
	// GlyphWarn marks a warning.
	GlyphWarn
	// GlyphInfo marks an informational line.
	GlyphInfo
	// GlyphBullet marks a list item.
	GlyphBullet
	// GlyphArrow marks user input echoes and drill-downs.
	GlyphArrow
	// GlyphRunning marks an in-progress action.
	GlyphRunning
	// GlyphAssistant marks assistant text in compact timelines.
	GlyphAssistant
	// GlyphEllipsis marks truncation.
	GlyphEllipsis
)

// glyphTable maps each glyph to its Unicode form, ASCII fallback and role.
var glyphTable = [...]struct {
	unicode string
	ascii   string
	role    theme.Role
}{
	GlyphSuccess:   {"✓", "+", theme.RoleToolSuccess},
	GlyphError:     {"✗", "x", theme.RoleToolError},
	GlyphWarn:      {"▲", "!", theme.RoleStatus},
	GlyphInfo:      {"•", "i", theme.RoleMuted},
	GlyphBullet:    {"·", "-", theme.RoleMuted},
	GlyphArrow:     {"❯", ">", theme.RoleModelName},
	GlyphRunning:   {"↻", "~", theme.RoleAction},
	GlyphAssistant: {"◆", "*", theme.RoleReasoning},
	GlyphEllipsis:  {"…", "...", theme.RoleMuted},
}

// String returns the glyph's textual form under the active profile: Unicode
// on any capable terminal, the ASCII fallback on dumb terminals and pipes
// (mirroring how color spans vanish there).
func (g Glyph) String() string {
	if theme.ActiveProfile() <= theme.ProfileASCII {
		return glyphTable[g].ascii
	}
	return glyphTable[g].unicode
}

// unicodeForm returns the Unicode form regardless of profile, for
// composition inside already-measured strings (e.g. Truncate's ellipsis).
func (g Glyph) unicodeForm() string {
	return glyphTable[g].unicode
}

// Role returns the semantic color role the glyph carries.
func (g Glyph) Role() theme.Role {
	return glyphTable[g].role
}

// Sym renders the glyph in its role color — the everyday call.
func Sym(g Glyph) string {
	return Colorize(g.String(), g.Role())
}

// StripVS16 removes every U+FE0F variation selector from s. Used by
// renderers to sanitize legacy catalog values during the migration; the
// selector flips glyph width between terminals and breaks column math.
func StripVS16(s string) string {
	return strings.ReplaceAll(s, "️", "")
}
