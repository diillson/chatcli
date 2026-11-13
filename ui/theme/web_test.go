/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package theme

import (
	"encoding/json"
	"regexp"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var webRoleNames = []string{
	WebRolePrimary, WebRoleSecondary, WebRoleAccent, WebRoleMuted, WebRoleSuccess, WebRoleWarning,
	WebRoleDanger, WebRoleInfo, WebRoleBorder, WebRoleText, WebRoleTextStrong, WebRoleBackground,
	WebRoleSurface, WebRoleSurface2, WebRoleOnPrimary,
}

// The export is the palette, not a copy that can drift: every palette role
// of every theme arrives under its name with the exact hex the terminal uses.
func TestWebExport_RolesMatchThePalette(t *testing.T) {
	hex := regexp.MustCompile(`^#[0-9A-F]{6}$`)
	for _, name := range Names() {
		th := builtins[name]()
		w := WebExport(th)
		assert.Equal(t, name, w.Name)
		require.Lenf(t, w.Roles, len(webRoleNames), "%s: role set", name)
		for _, r := range webRoleNames {
			assert.Regexpf(t, hex, w.Roles[r], "%s: role %s", name, r)
		}
		for role, want := range map[string]Color{
			WebRolePrimary: th.ColorFor(RoleModelName), WebRoleSecondary: th.ColorFor(RoleMultiAgent),
			WebRoleAccent: th.ColorFor(RoleReasoning), WebRoleMuted: th.ColorFor(RoleMuted),
			WebRoleSuccess: th.ColorFor(RoleToolSuccess), WebRoleWarning: th.ColorFor(RoleWarning),
			WebRoleDanger: th.ColorFor(RoleToolError), WebRoleInfo: th.ColorFor(RoleStatus),
			WebRoleBorder: th.ColorFor(RoleBorder), WebRoleText: th.ColorFor(RoleText),
			WebRoleTextStrong: th.ColorFor(RoleTextStrong), WebRoleBackground: th.ColorFor(RoleBackground),
			WebRoleOnPrimary: th.Palette.Background,
		} {
			assert.Equalf(t, want.Hex, w.Roles[role], "%s: role %s", name, role)
		}
		wantVariant := "dark"
		if th.Variant == VariantLight {
			wantVariant = "light"
		}
		assert.Equal(t, wantVariant, w.Variant, name)
		assert.Equal(t, th.Monoglyph, w.Mono, name)
		assert.Equal(t, th.Layout == LayoutFlat, w.Flat, name)
	}
}

// Only the monochrome, flat themes are offered to the pages; a colorful
// theme is absent, which is what makes a page keep its default look.
func TestWebThemes_AreTheMonochromeOnes(t *testing.T) {
	got := WebThemes()
	names := make([]string, 0, len(got))
	for n, w := range got {
		names = append(names, n)
		assert.True(t, w.Mono && w.Flat, n)
	}
	sort.Strings(names)
	assert.Equal(t, []string{"onyx", "onyx-light"}, names)
	assert.Equal(t, "#000000", got["onyx"].Roles[WebRoleBackground])
	assert.Equal(t, "#FFFFFF", got["onyx"].Roles[WebRolePrimary])
	assert.Equal(t, "#3FCF8E", got["onyx"].Roles[WebRoleSuccess])
	assert.Equal(t, "#FFFFFF", got["onyx-light"].Roles[WebRoleBackground])
}

func webColor(t *testing.T, w WebTheme, role string) Color {
	t.Helper()
	c, ok := w.Roles[role]
	require.Truef(t, ok, "%s: role %s missing", w.Name, role)
	return Color{Hex: c}
}

// The pages set text in every one of these inks on every one of the three
// grounds (page, panel, hover fill), at body size: WCAG AA is 4.5:1. A
// palette change that breaks a page fails here, not in a browser.
func TestWebThemes_TextContrastOnEveryGround(t *testing.T) {
	inks := []string{
		WebRoleText, WebRoleTextStrong, WebRolePrimary, WebRoleSecondary, WebRoleAccent, WebRoleInfo,
		WebRoleMuted, WebRoleSuccess, WebRoleWarning, WebRoleDanger,
	}
	grounds := []string{WebRoleBackground, WebRoleSurface, WebRoleSurface2}
	themes := WebThemes()
	require.NotEmpty(t, themes)
	for name, w := range themes {
		for _, g := range grounds {
			for _, ink := range inks {
				got := contrastRatio(webColor(t, w, ink), webColor(t, w, g))
				assert.GreaterOrEqualf(t, got, 4.5, "%s: %s (%s) on %s (%s) is %.2f:1",
					name, ink, w.Roles[ink], g, w.Roles[g], got)
			}
		}
		// The primary button: ground-colored ink on a Primary fill.
		got := contrastRatio(webColor(t, w, WebRoleOnPrimary), webColor(t, w, WebRolePrimary))
		assert.GreaterOrEqualf(t, got, 4.5, "%s: on_primary on primary is %.2f:1", name, got)
	}
}

// Borders and graph strokes that carry a state (running, done, failed,
// blocked, idle edge) are drawn in these roles: WCAG asks 3:1 against the
// ground for graphics that convey information. The Border role is exempt on
// purpose, as in contrast_test.go: it is a quiet divider, never a state.
func TestWebThemes_StateStrokesContrast(t *testing.T) {
	for name, w := range WebThemes() {
		for _, g := range []string{WebRoleBackground, WebRoleSurface, WebRoleSurface2} {
			for _, stroke := range []string{WebRolePrimary, WebRoleMuted, WebRoleSuccess, WebRoleWarning, WebRoleDanger} {
				got := contrastRatio(webColor(t, w, stroke), webColor(t, w, g))
				assert.GreaterOrEqualf(t, got, 3.0, "%s: stroke %s on %s is %.2f:1", name, stroke, g, got)
			}
		}
		// A surface must stay a surface: closer to the ground than to the text.
		bg, text := webColor(t, w, WebRoleBackground), webColor(t, w, WebRoleText)
		for _, s := range []string{WebRoleSurface, WebRoleSurface2} {
			assert.Lessf(t, contrastRatio(webColor(t, w, s), bg), 1.25, "%s: %s drifted from the ground", name, s)
			assert.Greaterf(t, contrastRatio(webColor(t, w, s), text), 4.5, "%s: %s too close to the text", name, s)
		}
	}
}

func TestActiveWebPayload_FollowsTheActiveTheme(t *testing.T) {
	t.Cleanup(restoreThemeState())

	require.NoError(t, SetActive("dracula"))
	p := ActiveWebPayload()
	assert.Equal(t, "dracula", p.Active)
	_, offered := p.Themes[p.Active]
	assert.False(t, offered, "a colorful theme must not be offered to the pages")

	require.NoError(t, SetActive("onyx-light"))
	p = ActiveWebPayload()
	assert.Equal(t, "onyx-light", p.Active)
	assert.Equal(t, "light", p.Themes[p.Active].Variant)

	raw, err := json.Marshal(p)
	require.NoError(t, err)
	var back struct {
		Active string `json:"active"`
		Themes map[string]struct {
			Roles map[string]string `json:"roles"`
			Mono  bool              `json:"mono"`
			Flat  bool              `json:"flat"`
		} `json:"themes"`
	}
	require.NoError(t, json.Unmarshal(raw, &back))
	assert.Equal(t, "onyx-light", back.Active)
	assert.Equal(t, OnyxTheme().Palette.Danger.Hex, back.Themes["onyx"].Roles["danger"])
	assert.True(t, back.Themes["onyx"].Mono && back.Themes["onyx"].Flat)
}

func TestMixHex(t *testing.T) {
	black, white := Color{Hex: "#000000"}, Color{Hex: "#FFFFFF"}
	assert.Equal(t, "#000000", mixHex(black, white, 0))
	assert.Equal(t, "#FFFFFF", mixHex(black, white, 1))
	assert.Equal(t, "#808080", mixHex(black, white, 0.5))
	assert.Equal(t, "#F3F3F3", mixHex(white, Color{Hex: "#171717"}, webSurface2Mix))
}
