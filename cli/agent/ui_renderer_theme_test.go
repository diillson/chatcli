/*
 * ChatCLI - theme integration for the card renderer
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Proves the central wiring (Colorize → theme.Recolor, ansiColorToLip →
 * theme.LipFromANSI) makes existing card call sites theme-aware without
 * being rewritten: a card built with the legacy agent.ColorCyan must adopt
 * the active theme's accent hue, and re-skin when the theme switches.
 */
package agent

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/ui/theme"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestRenderTimelineEvent_IsThemeAware(t *testing.T) {
	t.Cleanup(func() {
		_ = theme.SetActive("dark")
		theme.SetProfile(theme.ProfileANSI)
	})
	theme.SetProfile(theme.ProfileTrueColor)
	r := NewUIRendererWithStyle(zap.NewNop(), UIStyleFull)

	require.NoError(t, theme.SetActive("dark"))
	dark := captureEnvStdout(t, func() {
		r.RenderTimelineEvent("🧠", "REASONING", "pensando…", ColorCyan)
	})
	// Dark Accent (#38BDF8 → 56;189;248) must appear (border and/or title).
	assert.Contains(t, dark, "56;189;248", "card adopts the dark accent hue")

	require.NoError(t, theme.SetActive("light"))
	light := captureEnvStdout(t, func() {
		r.RenderTimelineEvent("🧠", "REASONING", "pensando…", ColorCyan)
	})
	// Light Accent is a different hue (#0891B2 → 8;145;178).
	assert.Contains(t, light, "8;145;178", "card adopts the light accent hue")
	assert.NotEqual(t, dark, light, "the same card re-skins when the theme switches")
}

func TestColorize_StripsColorWhenNoTTY(t *testing.T) {
	t.Cleanup(func() { theme.SetProfile(theme.DetectProfile()) })
	theme.SetProfile(theme.ProfileNoTTY)
	r := NewUIRendererWithStyle(zap.NewNop(), UIStyleFull)

	out := r.Colorize("hello", ColorYellow+ColorBold)
	assert.False(t, strings.Contains(out, "\033["), "no-color profile yields plain text: %q", out)
	assert.Equal(t, "hello", out)
}
