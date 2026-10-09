/*
 * ChatCLI - startup banner gradient tests
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/diillson/chatcli/ui/theme"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withThemeState runs f under the given theme and profile and restores the
// previous ones afterwards.
func withThemeState(t *testing.T, name string, p theme.Profile, f func()) {
	t.Helper()
	prevTheme, prevProfile := theme.Active().Name, theme.ActiveProfile()
	require.NoError(t, theme.SetActive(name))
	theme.SetProfile(p)
	t.Cleanup(func() {
		_ = theme.SetActive(prevTheme)
		theme.SetProfile(prevProfile)
	})
	f()
}

var trueColorFg = regexp.MustCompile(`\x1b\[38;2;(\d+);(\d+);(\d+)m█`)

func TestPrintLogo_TrueColorPaintsALeftToRightGradient(t *testing.T) {
	withThemeState(t, "dark", theme.ProfileTrueColor, func() {
		out := captureStdout(t, printLogo)

		cells := trueColorFg.FindAllStringSubmatch(out, -1)
		require.NotEmpty(t, cells, "every block cell carries its own truecolor escape")
		assert.Equal(t, strings.Count(stripANSIWelcome(out), "█"), len(cells), "no block cell is left uncolored")

		// The first row starts on the coral end of the brand ramp and ends
		// toward amber: green rises left to right.
		first := strings.SplitN(out, "\n", 2)[0]
		row := trueColorFg.FindAllStringSubmatch(first, -1)
		require.GreaterOrEqual(t, len(row), 2)
		g0, _ := strconv.Atoi(row[0][2])
		gN, _ := strconv.Atoi(row[len(row)-1][2])
		assert.Less(t, g0, gN, "the gradient runs coral to amber")

		plain := stripANSIWelcome(out)
		for _, glyph := range []string{"╔", "╝", "║", "╚", "╗", "═"} {
			assert.Contains(t, plain, glyph)
		}
	})
}

func TestPrintLogo_ANSI256UsesPaletteIndices(t *testing.T) {
	withThemeState(t, "dark", theme.ProfileANSI256, func() {
		out := captureStdout(t, printLogo)
		assert.NotContains(t, out, "38;2;", "a 256-color terminal never gets truecolor escapes")
		idx := regexp.MustCompile(`\x1b\[38;5;(\d+)m█`).FindAllStringSubmatch(out, -1)
		require.NotEmpty(t, idx)
		seen := map[string]bool{}
		for _, m := range idx {
			n, _ := strconv.Atoi(m[1])
			assert.True(t, n >= 16 && n <= 255, "index %d is outside the extended palette", n)
			seen[m[1]] = true
		}
		assert.Greater(t, len(seen), 1, "the gradient spans more than one palette entry")
	})
}

func TestPrintLogo_LowerProfilesKeepTheClassicBanner(t *testing.T) {
	for _, p := range []theme.Profile{theme.ProfileANSI, theme.ProfileASCII, theme.ProfileNoTTY} {
		withThemeState(t, "dark", p, func() {
			_, ok := gradientLogo([]string{"██╗"}, p, bannerRampDark)
			assert.False(t, ok, "profile %s must not get a gradient", p)

			out := captureStdout(t, printLogo)
			assert.NotContains(t, out, "38;2;")
			assert.NotContains(t, out, "38;5;")
			assert.Contains(t, stripANSIWelcome(out), "██")
		})
	}
}

func TestBannerRampFor_FollowsTheTheme(t *testing.T) {
	assert.Equal(t, bannerRampDark, bannerRampFor(theme.DarkTheme()))
	assert.Equal(t, bannerRampLight, bannerRampFor(theme.LightTheme()))

	d := theme.DraculaTheme()
	r := bannerRampFor(d)
	a, _ := hexRGB(d.Palette.Primary.Hex)
	c, _ := hexRGB(d.Palette.Secondary.Hex)
	assert.Equal(t, a, r[0], "other themes start on their Primary")
	assert.Equal(t, c, r[2], "and end on their Secondary")
}

func TestBannerRamp_EndpointsAndMiddle(t *testing.T) {
	assert.Equal(t, bannerRampDark[0], bannerRampDark.at(0))
	assert.Equal(t, bannerRampDark[1], bannerRampDark.at(0.55))
	assert.Equal(t, bannerRampDark[2], bannerRampDark.at(1))
	assert.Equal(t, bannerRampDark[2], bannerRampDark.at(2), "t is clamped")
}

func TestXterm256(t *testing.T) {
	assert.Equal(t, 196, xterm256(255, 0, 0))
	assert.Equal(t, 231, xterm256(255, 255, 255))
	assert.Equal(t, 16, xterm256(0, 0, 0))
	assert.Equal(t, 244, xterm256(128, 128, 128), "mid gray maps to the grayscale ramp")
}

func TestHexRGB(t *testing.T) {
	c, ok := hexRGB("#FF8A5C")
	assert.True(t, ok)
	assert.Equal(t, [3]float64{255, 138, 92}, c)
	for _, bad := range []string{"", "FF8A5C", "#FF8A5", "#GG0000"} {
		_, ok := hexRGB(bad)
		assert.False(t, ok, bad)
	}
}
