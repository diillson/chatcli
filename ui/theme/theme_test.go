/*
 * ChatCLI - tests for the unified theme package
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The load-bearing invariant: at the 16-color profile, the dark theme must
 * emit the EXACT SGR codes ChatCLI used before the theme system existed.
 * That is what makes adopting the theme a visually neutral change on legacy
 * terminals. The other tests lock the degradation ladder and the glamour
 * style derivation.
 */
package theme

import (
	"strings"
	"testing"

	"github.com/charmbracelet/glamour"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// legacyANSI is the pre-theme constant table (cli/colors.go) keyed by role,
// so we assert the dark theme reproduces it byte for byte at ProfileANSI.
func TestDarkTheme_ANSI16_MatchesLegacy(t *testing.T) {
	dark := DarkTheme()
	cases := []struct {
		role Role
		want string
		name string
	}{
		{RoleModelName, "\033[35m", "model name → purple"},
		{RoleReasoning, "\033[36m", "reasoning → cyan"},
		{RoleExplanation, "\033[92m", "explanation → lime (bright green)"},
		{RoleResponse, "\033[90m", "response → gray (bright black)"},
		{RoleBorder, "\033[90m", "border → gray"},
		{RoleToolSuccess, "\033[32m", "tool success → green"},
		{RoleToolError, "\033[31m", "tool error → red"},
		{RoleStatus, "\033[92m", "status → lime"},
		{RoleAction, "\033[35m", "action → purple"},
	}
	for _, c := range cases {
		got := dark.ANSIFor(c.role, ProfileANSI)
		assert.Equalf(t, c.want, got, "%s: ANSI16 must match legacy SGR", c.name)
	}
}

// TestColorSGR_DegradationLadder locks each profile's escape shape.
func TestColorSGR_DegradationLadder(t *testing.T) {
	c := Color{Hex: "#38BDF8", ANSI256: 45, ANSI16: 6}
	assert.Equal(t, "\033[38;2;56;189;248m", c.SGR(ProfileTrueColor), "truecolor → 24-bit")
	assert.Equal(t, "\033[38;5;45m", c.SGR(ProfileANSI256), "256-color → 8-bit index")
	assert.Equal(t, "\033[36m", c.SGR(ProfileANSI), "16-color → classic SGR")
	assert.Equal(t, "", c.SGR(ProfileAscii), "ascii → no escape")
	assert.Equal(t, "", c.SGR(ProfileNoTTY), "no-tty → no escape")
}

// TestSGR16_BrightRange verifies the 8–15 index range maps to 90–97, the
// exact rule the legacy gray/lime constants depended on.
func TestSGR16_BrightRange(t *testing.T) {
	assert.Equal(t, "\033[90m", Color{ANSI16: 8}.SGR(ProfileANSI), "index 8 → 90 (bright black)")
	assert.Equal(t, "\033[92m", Color{ANSI16: 10}.SGR(ProfileANSI), "index 10 → 92 (bright green)")
	assert.Equal(t, "\033[37m", Color{ANSI16: 7}.SGR(ProfileANSI), "index 7 → 37 (white)")
}

// TestRegistry_SetActive switches themes and rejects unknown names.
func TestRegistry_SetActive(t *testing.T) {
	t.Cleanup(func() { _ = SetActive(config_defaultName) })

	require.NoError(t, SetActive("light"))
	assert.Equal(t, "light", Active().Name)
	assert.Equal(t, VariantLight, Active().Variant)

	require.NoError(t, SetActive("dark"))
	assert.Equal(t, "dark", Active().Name)

	err := SetActive("solarized-nope")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown theme")
	assert.Equal(t, "dark", Active().Name, "rejected switch must not change the active theme")
}

// TestLipFromANSI_BridgesLegacyCodes proves the retro-compat bridge maps the
// legacy ANSI strings to the active palette (so card borders re-theme without
// call-site changes).
func TestLipFromANSI_BridgesLegacyCodes(t *testing.T) {
	t.Cleanup(func() { _ = SetActive("dark"); SetProfile(DetectProfile()) })
	require.NoError(t, SetActive("dark"))
	SetProfile(ProfileTrueColor)

	// Cyan legacy code must resolve to the dark Accent hex.
	got := LipFromANSI("\033[36m")
	assert.Equal(t, string(DarkTheme().Palette.Accent.Lip(ProfileTrueColor)), string(got))

	// Unknown code → terminal default (empty).
	assert.Equal(t, "", string(LipFromANSI("\033[99m")))
}

// TestLipFromANSI_SharesBasicTable guards the de-duplication: LipFromANSI must
// resolve every legacy basic code to exactly what basicFgColor (the single
// shared table that Recolor uses) yields, so the two paths cannot drift.
func TestLipFromANSI_SharesBasicTable(t *testing.T) {
	t.Cleanup(restoreThemeState())
	require.NoError(t, SetActive("dark"))
	SetProfile(ProfileTrueColor)

	codes := map[string]string{
		"\033[31m": "31", "\033[32m": "32", "\033[33m": "33", "\033[34m": "34",
		"\033[35m": "35", "\033[36m": "36", "\033[90m": "90", "\033[92m": "92",
	}
	for seq, param := range codes {
		want, ok := basicFgColor(param)
		require.Truef(t, ok, "basicFgColor(%q) should resolve", param)
		assert.Equalf(t, string(want.Lip(ProfileTrueColor)), string(LipFromANSI(seq)),
			"LipFromANSI(%q) must match the shared basicFgColor table", seq)
	}
}

// TestProfile_HasColorAndString locks the profile helpers used by /config.
func TestProfile_HasColorAndString(t *testing.T) {
	assert.True(t, ProfileTrueColor.HasColor())
	assert.True(t, ProfileANSI.HasColor())
	assert.False(t, ProfileAscii.HasColor())
	assert.False(t, ProfileNoTTY.HasColor())
	assert.Equal(t, "truecolor", ProfileTrueColor.String())
	assert.Equal(t, "no-tty", ProfileNoTTY.String())
}

// TestGlamourStyleConfig_RendersAndIsThemed ensures the generated style is
// valid (glamour accepts it) and that the active theme actually drives it:
// switching to light must produce different bytes for the same markdown.
func TestGlamourStyleConfig_RendersAndIsThemed(t *testing.T) {
	t.Cleanup(func() { _ = SetActive("dark") })

	md := "# Título\n\n```go\nfunc main() {}\n```\n"

	require.NoError(t, SetActive("dark"))
	darkR, err := glamour.NewTermRenderer(Active().GlamourOptions(ProfileTrueColor)...)
	require.NoError(t, err)
	darkOut, err := darkR.Render(md)
	require.NoError(t, err)
	require.NotEmpty(t, strings.TrimSpace(darkOut))

	require.NoError(t, SetActive("light"))
	lightR, err := glamour.NewTermRenderer(Active().GlamourOptions(ProfileTrueColor)...)
	require.NoError(t, err)
	lightOut, err := lightR.Render(md)
	require.NoError(t, err)

	assert.NotEqual(t, darkOut, lightOut, "themes must produce visibly different markdown")
}

// TestThemes_StateRoleDistinctOn16Color guards against semantic roles
// collapsing to the same 16-color index — e.g. status (Info) and tool-success
// (Success) must stay distinguishable on a 16-color terminal in BOTH themes.
func TestThemes_StateRoleDistinctOn16Color(t *testing.T) {
	for _, mk := range []func() Theme{DarkTheme, LightTheme} {
		th := mk()
		info := th.ColorFor(RoleStatus).ANSI16
		success := th.ColorFor(RoleToolSuccess).ANSI16
		assert.NotEqualf(t, success, info,
			"%s: Info(%d) and Success(%d) must differ on 16-color terminals",
			th.Name, info, success)
	}
}

// config_defaultName mirrors config.DefaultTheme without importing config in
// the test (keeps the test focused on the theme package surface).
const config_defaultName = "dark"
