/*
 * ChatCLI - legibility contract for every built-in theme
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * A theme may be as opinionated as it likes about hue; it may not be
 * unreadable. These tests encode the two ways a palette actually breaks in
 * the field:
 *
 *   1. Truecolor: a TEXT role too close in luminance to the theme's own
 *      ground. This is what made secondary lines vanish under nord/one-dark
 *      (comment colors reused for whole metadata lines).
 *   2. 16-color: an index from the wrong half of the range. The bright range
 *      (9–15) is painted for a dark ground — bright green on a white terminal
 *      is invisible — and index 0 is black, invisible on a dark one. This is
 *      the same failure as the input line that was hardcoded to prompt.White.
 *
 * Border is deliberately exempt from the contrast floor: every upstream
 * palette here (Nord, Solarized, Catppuccin, One Dark) defines its border /
 * gutter tone as a near-ground fill on purpose, and "fixing" it would replace
 * the palette's identity with our own.
 */
package theme

import (
	"fmt"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

// relativeLuminance implements WCAG 2.x relative luminance for an sRGB color.
func relativeLuminance(c Color) float64 {
	r, g, b := c.rgb()
	lin := func(v uint8) float64 {
		f := float64(v) / 255
		if f <= 0.03928 {
			return f / 12.92
		}
		return math.Pow((f+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(r) + 0.7152*lin(g) + 0.0722*lin(b)
}

// contrastRatio is the WCAG contrast between two palette colors.
func contrastRatio(a, b Color) float64 {
	la, lb := relativeLuminance(a), relativeLuminance(b)
	hi, lo := math.Max(la, lb), math.Min(la, lb)
	return (hi + 0.05) / (lo + 0.05)
}

// TestThemes_TextRolesAreLegible holds every palette's TEXT tones against its
// own declared ground. Body text takes the WCAG AA floor (4.5:1); Muted is
// secondary text and takes the large-text floor (3:1), which is where the
// four dark palettes that reused their comment color used to fail.
func TestThemes_TextRolesAreLegible(t *testing.T) {
	t.Cleanup(restoreThemeState())

	for _, name := range Names() {
		th := builtins[name]()
		p := th.Palette
		for _, tc := range []struct {
			role string
			c    Color
			min  float64
		}{
			{"Text", p.Text, 4.5},
			{"TextStrong", p.TextStrong, 4.5},
			{"Muted", p.Muted, 3.0},
		} {
			got := contrastRatio(tc.c, p.Background)
			assert.GreaterOrEqualf(t, got, tc.min,
				"%s: %s (%s) is %.2f:1 on the theme's ground (%s) — below the %.1f:1 floor",
				name, tc.role, tc.c.Hex, got, p.Background.Hex, tc.min)
		}
	}
}

// TestThemes_ANSI16MatchesVariant guards the 16-color fallback against the
// bug class the light theme shipped with: Info at index 10 (bright green),
// which is deliberately painted for a dark ground and disappears on a light
// terminal. A light palette must stay in 0–8; a dark one must never use 0.
func TestThemes_ANSI16MatchesVariant(t *testing.T) {
	t.Cleanup(restoreThemeState())

	for _, name := range Names() {
		th := builtins[name]()
		p := th.Palette
		// Background is exempt in both directions: it IS the ground, so a
		// light theme's 15 and a dark theme's 0 are the correct values.
		roles := map[string]Color{
			"Primary": p.Primary, "Secondary": p.Secondary, "Accent": p.Accent,
			"Muted": p.Muted, "Success": p.Success, "Warning": p.Warning,
			"Danger": p.Danger, "Info": p.Info, "Border": p.Border,
			"Text": p.Text, "TextStrong": p.TextStrong,
		}
		for role, c := range roles {
			if th.Variant == VariantLight {
				assert.LessOrEqualf(t, int(c.ANSI16), 8,
					"%s (light): %s uses the bright index %d, which is painted for a DARK ground",
					name, role, c.ANSI16)
				continue
			}
			assert.NotEqualf(t, uint8(0), c.ANSI16,
				"%s (dark): %s uses index 0 (black), invisible on a dark ground", name, role)
		}
	}
}

// TestThemes_DropdownPairsContrast locks the fill/ink pairings the REPL
// completion dropdown uses (cli/prompt_theme.go). go-prompt paints a filled
// row, so the ink must be chosen against the FILL, never the terminal — the
// asymmetry that makes "TextStrong everywhere" print black on dark gray under
// every light theme.
func TestThemes_DropdownPairsContrast(t *testing.T) {
	t.Cleanup(restoreThemeState())

	for _, name := range Names() {
		p := builtins[name]().Palette
		for _, pair := range []struct {
			what     string
			ink, fil Color
		}{
			{"suggestion", p.TextStrong, p.Border},
			{"selected suggestion", p.Background, p.Secondary},
			{"description", p.Warning, p.Background},
			{"selected description", p.TextStrong, p.Border},
		} {
			got := contrastRatio(pair.ink, pair.fil)
			assert.GreaterOrEqualf(t, got, 3.0,
				"%s: dropdown %s ink %s on fill %s is only %.2f:1",
				name, pair.what, pair.ink.Hex, pair.fil.Hex, got)
		}
	}
}

// TestThemes_ANSI16SurfacesStayOnTheirSide is the 16-color half of the
// dropdown contract: on a classic terminal the fill and the ink collapse to
// indices, and a fill that lands on the SAME index as its ink renders an
// unreadable row regardless of what the hex values promise.
func TestThemes_ANSI16SurfacesStayOnTheirSide(t *testing.T) {
	t.Cleanup(restoreThemeState())

	for _, name := range Names() {
		p := builtins[name]().Palette
		pairs := map[string][2]Color{
			"suggestion":           {p.TextStrong, p.Border},
			"selected suggestion":  {p.Background, p.Secondary},
			"description":          {p.Warning, p.Background},
			"selected description": {p.TextStrong, p.Border},
		}
		for what, pair := range pairs {
			assert.NotEqualf(t, pair[0].ANSI16, pair[1].ANSI16,
				"%s: dropdown %s collapses ink and fill onto index %d at the 16-color profile",
				name, what, pair[0].ANSI16)
		}
	}
}

// TestNewRoles_ResolveToTheirPaletteEntry pins the roles added for the
// surfaces that paint their own text (menus, listings) and for the go-prompt
// bridge, so a future reshuffle of the Role enum cannot silently repoint them.
func TestNewRoles_ResolveToTheirPaletteEntry(t *testing.T) {
	for _, name := range Names() {
		th := builtins[name]()
		assert.Equal(t, th.Palette.Text, th.ColorFor(RoleText), name)
		assert.Equal(t, th.Palette.TextStrong, th.ColorFor(RoleTextStrong), name)
		assert.Equal(t, th.Palette.Background, th.ColorFor(RoleBackground), name)
	}
}

// TestGrafite_IsRegistered documents the palette that shipped without being
// wired into the surfaces that enumerate themes.
func TestGrafite_IsRegistered(t *testing.T) {
	t.Cleanup(restoreThemeState())
	_, ok := builtins["grafite"]
	assert.True(t, ok, "grafite must stay registered")
	assert.Contains(t, fmt.Sprint(Names()), "grafite")
}
