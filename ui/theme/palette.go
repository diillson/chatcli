/*
 * ChatCLI - Unified theme / palette
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Package theme is the single source of truth for ChatCLI's colors. It is a
 * LEAF package: it depends only on charmbracelet rendering libs and must not
 * import cli or cli/agent, so both of those (and any future surface) can
 * import it without an import cycle. Historically the ANSI color constants
 * were duplicated in cli/colors.go and cli/agent/ui_renderer.go precisely
 * because there was no shared low-level home for them — this package is it.
 *
 * A Color carries three representations so that color degradation is
 * DETERMINISTIC rather than left to a library's automatic hex→index guess
 * (which drifts on near-grays and limes): a truecolor Hex (source of truth),
 * an explicit ANSI256 index, and an explicit ANSI16 index. The ANSI16 path
 * reproduces, byte for byte, the SGR codes ChatCLI emitted before the theme
 * system existed — that is what keeps the migration visually neutral.
 */
package theme

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
)

// Color is one palette entry with explicit representations for every color
// profile we support. Keeping all three avoids non-deterministic downgrades.
type Color struct {
	Hex     string // "#7C3AED" — truecolor source of truth
	ANSI256 uint8  // 0–255 index, emitted on ANSI256 terminals
	ANSI16  uint8  // 0–15 index, emitted on 16-color terminals (classic SGR)
}

// SGR returns the foreground escape sequence for this color under the given
// profile, including the leading ESC and trailing "m". On Ascii/NoTTY it
// returns "" so piped/CI output stays plain text.
func (c Color) SGR(p Profile) string {
	switch p {
	case ProfileTrueColor:
		r, g, b := c.rgb()
		return fmt.Sprintf("\033[38;2;%d;%d;%dm", r, g, b)
	case ProfileANSI256:
		return fmt.Sprintf("\033[38;5;%dm", c.ANSI256)
	case ProfileANSI:
		return sgr16(c.ANSI16, false)
	default: // ProfileAscii / ProfileNoTTY
		return ""
	}
}

// SGRBg mirrors SGR for background colors (used for code-block backgrounds).
func (c Color) SGRBg(p Profile) string {
	switch p {
	case ProfileTrueColor:
		r, g, b := c.rgb()
		return fmt.Sprintf("\033[48;2;%d;%d;%dm", r, g, b)
	case ProfileANSI256:
		return fmt.Sprintf("\033[48;5;%dm", c.ANSI256)
	case ProfileANSI:
		return sgr16(c.ANSI16, true)
	default:
		return ""
	}
}

// Lip returns a lipgloss.Color appropriate for the profile. On truecolor we
// hand lipgloss the hex; on lower profiles we hand it the explicit index so
// our deterministic choice wins over lipgloss's automatic conversion. On
// Ascii/NoTTY we return the empty color (terminal default / no styling).
func (c Color) Lip(p Profile) lipgloss.Color {
	switch p {
	case ProfileTrueColor:
		return lipgloss.Color(c.Hex)
	case ProfileANSI256:
		return lipgloss.Color(fmt.Sprintf("%d", c.ANSI256))
	case ProfileANSI:
		return lipgloss.Color(fmt.Sprintf("%d", c.ANSI16))
	default:
		return lipgloss.Color("")
	}
}

// rgb parses the Hex ("#rrggbb") into 8-bit components. A malformed hex
// degrades to white rather than panicking — a wrong color is a cosmetic bug,
// a panic in the render path is not acceptable.
func (c Color) rgb() (uint8, uint8, uint8) {
	var r, g, b uint8
	if len(c.Hex) == 7 && c.Hex[0] == '#' {
		if _, err := fmt.Sscanf(c.Hex, "#%02x%02x%02x", &r, &g, &b); err == nil {
			return r, g, b
		}
	}
	return 255, 255, 255
}

// sgr16 builds a classic 16-color SGR sequence from a 0–15 index. Indices
// 0–7 map to the 30–37 range; 8–15 map to the bright 90–97 range. This is the
// exact mapping ChatCLI's legacy constants used (e.g. index 8 → "\033[90m"),
// so the ANSI profile is byte-identical to the pre-theme output.
func sgr16(idx uint8, bg bool) string {
	base := 30
	if bg {
		base = 40
	}
	if idx <= 7 {
		return fmt.Sprintf("\033[%dm", base+int(idx))
	}
	// bright range: 90–97 (fg) / 100–107 (bg)
	return fmt.Sprintf("\033[%dm", base+60+int(idx-8))
}

// Palette holds SEMANTIC colors — roles, not hues. Call sites ask for
// "the reasoning color", never "cyan", so a theme swap re-skins everything
// without touching a single call site.
type Palette struct {
	// Brand / hierarchy
	Primary   Color // model name, primary actions
	Secondary Color // multi-agent, batch, secondary badges
	Accent    Color // reasoning / cognitive emphasis
	Muted     Color // neutral borders, secondary text, defaults

	// Semantic state
	Success Color // tool success, enabled
	Warning Color // warnings, disabled
	Danger  Color // errors, tool failure
	Info    Color // notes / explanations

	// Structural
	Border     Color // default card border
	Text       Color // body text
	TextStrong Color // bold / headings
	Background Color // code-block background
}

// Variant distinguishes dark vs light builds of a palette.
type Variant int

const (
	VariantDark Variant = iota
	VariantLight
)

// Theme bundles a named palette with its variant.
type Theme struct {
	Name    string
	Variant Variant
	Palette Palette
}
