/*
 * ChatCLI - tests for legacy ANSI recoloring
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package theme

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecolor_NoOpAtANSIProfile(t *testing.T) {
	t.Cleanup(restoreThemeState())
	require.NoError(t, SetActive("dark"))
	SetProfile(ProfileANSI)

	// At 16-color, every hue's ANSI16 index equals the legacy code, so the
	// remap must be byte-for-byte identical — the legacy-terminal guarantee.
	in := "\033[36mcyan\033[0m \033[33myellow\033[0m \033[90mgray\033[0m"
	assert.Equal(t, in, Recolor(in))
}

func TestRecolor_UpgradesToTruecolor(t *testing.T) {
	t.Cleanup(restoreThemeState())
	require.NoError(t, SetActive("dark"))
	SetProfile(ProfileTrueColor)

	out := Recolor("\033[36mhi\033[0m")
	// Cyan → Accent #38BDF8 → 56;189;248
	assert.Contains(t, out, "\033[38;2;56;189;248m")
	assert.Contains(t, out, "hi")
	assert.Contains(t, out, "\033[0m", "reset is preserved")
}

func TestRecolor_StripsAllOnNoColor(t *testing.T) {
	t.Cleanup(restoreThemeState())
	SetProfile(ProfileNoTTY)

	out := Recolor("\033[36mhello\033[0m\033[1mbold\033[0m")
	assert.Equal(t, "hellobold", out, "no-color profile must yield clean plain text")
}

func TestRecolor_LeavesExtendedSequencesUntouched(t *testing.T) {
	t.Cleanup(restoreThemeState())
	require.NoError(t, SetActive("dark"))
	SetProfile(ProfileTrueColor)

	// Glamour-style extended code must pass through unchanged.
	in := "\033[38;5;188mx\033[0m"
	assert.Equal(t, in, Recolor(in))
}

func TestRecolor_FastPathNoEscapes(t *testing.T) {
	assert.Equal(t, "plain text", Recolor("plain text"))
}

// restoreThemeState returns a cleanup that restores the dark theme and the
// detected profile, so tests that mutate global theme state don't leak.
func restoreThemeState() func() {
	return func() {
		_ = SetActive("dark")
		SetProfile(DetectProfile())
	}
}
