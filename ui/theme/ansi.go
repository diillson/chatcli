/*
 * ChatCLI - Legacy ANSI recoloring
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The codebase colors most output by concatenating the historical 16-color
 * constants (ColorYellow, ColorCyan, …) and handing the result to a Colorize
 * helper. Rewriting all ~hundred call sites to a semantic API would be a large,
 * churn-heavy change for little gain. Instead, Recolor is applied centrally
 * inside those Colorize helpers: it rewrites the basic foreground escapes to
 * the ACTIVE theme's matching hue under the ACTIVE profile, so the whole UI
 * re-skins on a theme switch and gains truecolor depth on capable terminals —
 * with zero call-site changes.
 *
 * Two guarantees keep this safe:
 *   - At the 16-color profile the remap is a no-op (each hue's ANSI16 index
 *     equals the legacy code), so legacy terminals are byte-for-byte unchanged.
 *   - On a no-color profile (pipe/CI) every SGR sequence is stripped, so
 *     redirected output is clean plain text.
 *
 * Only single-parameter basic sequences ("\033[33m") are rewritten — exactly
 * the shape the app's own constants produce. Compound or extended sequences
 * (e.g. glamour's "\033[38;5;188m") are left untouched; glamour content is
 * themed at render time and never flows through these helpers anyway.
 */
package theme

import "strings"

// basicFgRole maps a single-parameter SGR foreground code to the semantic
// palette color it should adopt under the active theme.
func basicFgColor(param string) (Color, bool) {
	p := Active().Palette
	switch param {
	case "30", "90": // black / bright-black (gray)
		return p.Muted, true
	case "31", "91": // red
		return p.Danger, true
	case "32": // green
		return p.Success, true
	case "92": // bright green (lime)
		return p.Info, true
	case "33", "93": // yellow
		return p.Warning, true
	case "34", "94": // blue
		return p.Secondary, true
	case "35", "95": // magenta / purple
		return p.Primary, true
	case "36", "96": // cyan
		return p.Accent, true
	case "37": // white
		return p.Text, true
	case "97": // bright white
		return p.TextStrong, true
	default:
		return Color{}, false
	}
}

// Recolor rewrites legacy basic-color foreground escapes in s to the active
// theme + profile. See the file comment for the guarantees. It is allocation-
// free for the common case (no escapes, or a no-op profile with no escapes).
func Recolor(s string) string {
	if !strings.ContainsRune(s, '\033') {
		return s
	}
	prof := ActiveProfile()
	stripAll := !prof.HasColor()

	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != '\033' || i+1 >= len(s) || s[i+1] != '[' {
			b.WriteByte(s[i])
			i++
			continue
		}
		// Found CSI: find the terminating byte.
		end := i + 2
		for end < len(s) && (s[end] < 0x40 || s[end] > 0x7e) {
			end++
		}
		if end >= len(s) {
			// Malformed/truncated escape — copy verbatim and stop scanning.
			b.WriteString(s[i:])
			break
		}
		seq := s[i : end+1] // includes final byte
		final := s[end]
		if final == 'm' {
			if stripAll {
				// Drop the entire SGR sequence for clean no-color output.
				i = end + 1
				continue
			}
			param := s[i+2 : end] // params between "[" and "m"
			if c, ok := basicFgColor(param); ok {
				b.WriteString(c.SGR(prof))
				i = end + 1
				continue
			}
		}
		// Reset (\033[0m / \033[m), attributes, bg, extended, or non-SGR:
		// keep as-is — unless we are stripping all color, in which case any
		// 'm' sequence is dropped (handled above) and other finals are kept.
		b.WriteString(seq)
		i = end + 1
	}
	return b.String()
}
