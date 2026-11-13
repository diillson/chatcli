/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package kit

import (
	"testing"

	"github.com/diillson/chatcli/ui/theme"
	"github.com/mattn/go-runewidth"
)

// Every entry of every monoglyph map is replaced by exactly one cell, in
// the Unicode profile and in the ASCII one: a replacement of another width
// would shift every column measured before the rewrite.
func TestMonoglyphMapReplacementsAreOneCell(t *testing.T) {
	withTheme(t, "onyx")
	check := func(from rune, to string) {
		t.Helper()
		if w := runewidth.StringWidth(to); w != 1 {
			t.Errorf("%q (U+%04X) becomes %q, %d cells wide", from, from, to, w)
		}
	}
	for _, profile := range []theme.Profile{theme.ProfileTrueColor, theme.ProfileASCII} {
		theme.SetProfile(profile)
		for from, glyph := range monoStatus {
			check(from, glyph.String())
			if got := Monoglyph(string(from)); got != glyph.String() {
				t.Errorf("Monoglyph(%q) = %q, want the map's %q", from, got, glyph.String())
			}
		}
	}
	theme.SetProfile(theme.ProfileTrueColor)
	for from, to := range monoArrows {
		check(from, to)
	}
	for from, to := range monoSymbols {
		check(from, to)
		if got := Monoglyph(string(from) + " field"); got != to+" field" {
			t.Errorf("Monoglyph(%q) = %q, want %q kept with its space", from, got, to+" field")
		}
	}
	for from := range monoSymbols {
		if _, twice := monoStatus[from]; twice {
			t.Errorf("%q is in two maps", from)
		}
	}
	// The glyphs the flat timeline adds obey the same rule.
	for _, g := range []Glyph{GlyphDenied, GlyphGutter, GlyphGutterEnd} {
		for _, form := range []string{glyphTable[g].unicode, glyphTable[g].ascii} {
			if w := runewidth.StringWidth(form); w != 1 {
				t.Errorf("glyph %d form %q is %d cells wide", g, form, w)
			}
		}
	}
}

// Symbols that carry the message survive; text symbols in the block that
// terminals color are left alone, so list markers keep their column.
func TestMonoglyphKeepsMeaningfulSymbols(t *testing.T) {
	withTheme(t, "onyx")
	for in, want := range map[string]string{
		"🔒 api_key":      "▣ api_key",
		"[🌐 🅿2] ":        "[⊕ P2] ",
		"★ 1. brave":     "★ 1. brave",
		"☐ todo ☑ done":  "☐ todo ☑ done",
		"☁ cloud ⚙ gear": "cloud gear",
	} {
		if got := Monoglyph(in); got != want {
			t.Errorf("Monoglyph(%q) = %q, want %q", in, got, want)
		}
	}
}
