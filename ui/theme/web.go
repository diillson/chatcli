/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package theme

import "fmt"

// The browser pages (web UI, live dashboard, task graph) ship one look of
// their own and keep it under every colorful terminal theme. The monochrome
// themes are different: their whole point is one quiet palette everywhere,
// so the pages render them too. This file is the only place a page learns
// those colors from. A page maps the role names below onto its own CSS
// custom properties and never carries a second copy of the palette.

// Web role names. The first twelve are the palette entries one to one; the
// last four exist only for pages, which need a raised panel and a hover
// fill (a terminal has neither), an ink for text set on a Primary fill, and
// an edge for the controls a terminal does not draw.
const (
	WebRolePrimary    = "primary"
	WebRoleSecondary  = "secondary"
	WebRoleAccent     = "accent"
	WebRoleMuted      = "muted"
	WebRoleSuccess    = "success"
	WebRoleWarning    = "warning"
	WebRoleDanger     = "danger"
	WebRoleInfo       = "info"
	WebRoleBorder     = "border"
	WebRoleText       = "text"
	WebRoleTextStrong = "text_strong"
	WebRoleBackground = "background"
	WebRoleSurface    = "surface"
	WebRoleSurface2   = "surface2"
	WebRoleOnPrimary  = "on_primary"
	// WebRoleControl is the boundary of an interactive control (input,
	// select, button). The Border role is a divider and may stay faint; a
	// control must be findable without reading its label, so its edge is
	// held at 3:1 against every ground (WCAG 1.4.11).
	WebRoleControl = "control"
)

// The surfaces are the ground moved a few percent toward the text color.
// They stay this close on purpose: the state colors are also used as text on
// them, and a stronger step drops the light variant's warning under 4.5:1.
const (
	webSurfaceMix  = 0.025
	webSurface2Mix = 0.05
	// The control edge is the muted ink moved a fifth of the way back to the
	// ground: quieter than text, and still 3:1 on the hover fill of the
	// light variant, which is the pair with the least room.
	webControlMix = 0.20
)

// WebTheme is one theme flattened for a browser page.
type WebTheme struct {
	Name string `json:"name"`
	// Variant is "dark" or "light".
	Variant string `json:"variant"`
	// Mono asks for text or plain glyphs instead of emoji in page chrome.
	Mono bool `json:"mono"`
	// Flat asks for thin borders with no shadows, glows or gradients.
	Flat bool `json:"flat"`
	// Roles maps a web role name to "#RRGGBB".
	Roles map[string]string `json:"roles"`
}

// WebPayload is what a page receives: the terminal's theme by name, and
// every theme a page is able to render. A page shows its own default look
// whenever the name it wants is not among Themes.
type WebPayload struct {
	Active string              `json:"active"`
	Themes map[string]WebTheme `json:"themes"`
}

// WebExport flattens t for a page.
func WebExport(t Theme) WebTheme {
	p := t.Palette
	variant := "dark"
	if t.Variant == VariantLight {
		variant = "light"
	}
	return WebTheme{
		Name:    t.Name,
		Variant: variant,
		Mono:    t.Monoglyph,
		Flat:    t.Layout == LayoutFlat,
		Roles: map[string]string{
			WebRolePrimary:    p.Primary.Hex,
			WebRoleSecondary:  p.Secondary.Hex,
			WebRoleAccent:     p.Accent.Hex,
			WebRoleMuted:      p.Muted.Hex,
			WebRoleSuccess:    p.Success.Hex,
			WebRoleWarning:    p.Warning.Hex,
			WebRoleDanger:     p.Danger.Hex,
			WebRoleInfo:       p.Info.Hex,
			WebRoleBorder:     p.Border.Hex,
			WebRoleText:       p.Text.Hex,
			WebRoleTextStrong: p.TextStrong.Hex,
			WebRoleBackground: p.Background.Hex,
			WebRoleSurface:    mixHex(p.Background, p.Text, webSurfaceMix),
			WebRoleSurface2:   mixHex(p.Background, p.Text, webSurface2Mix),
			WebRoleOnPrimary:  p.Background.Hex,
			WebRoleControl:    mixHex(p.Muted, p.Background, webControlMix),
		},
	}
}

// WebThemes returns the themes a page renders in their own palette: the
// monochrome, flat ones. Every other theme keeps the page's default look.
func WebThemes() map[string]WebTheme {
	out := make(map[string]WebTheme)
	for name, ctor := range builtins {
		if t := ctor(); t.Monoglyph && t.Layout == LayoutFlat {
			out[name] = WebExport(t)
		}
	}
	return out
}

// ActiveWebPayload is the payload for the theme the process runs with now.
// Servers call it per request, so a page that polls follows a theme change.
func ActiveWebPayload() WebPayload {
	return WebPayload{Active: Active().Name, Themes: WebThemes()}
}

// mixHex moves a toward b by amount (0 keeps a, 1 gives b).
func mixHex(a, b Color, amount float64) string {
	ar, ag, ab := a.rgb()
	br, bg, bb := b.rgb()
	mix := func(x, y uint8) int {
		return int(float64(x) + (float64(y)-float64(x))*amount + 0.5)
	}
	return fmt.Sprintf("#%02X%02X%02X", mix(ar, br), mix(ag, bg), mix(ab, bb))
}
