/*
 * ChatCLI - tests for the go-prompt ↔ theme color bridge
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"errors"
	"testing"

	prompt "github.com/c-bata/go-prompt"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/ui/theme"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withTheme(t *testing.T, name string, prof theme.Profile) {
	t.Helper()
	prev := theme.Active().Name
	prevProf := theme.ActiveProfile()
	require.NoError(t, theme.SetActive(name))
	theme.SetProfile(prof)
	t.Cleanup(func() {
		_ = theme.SetActive(prev)
		theme.SetProfile(prevProf)
	})
}

// TestPromptColor_DarkThemeReproducesLegacyLiterals is the no-regression
// anchor: on the default dark theme the palette-derived colors must resolve to
// the exact go-prompt constants the prompt was hardcoded with, so adopting the
// bridge changes nothing on the theme most users run.
func TestPromptColor_DarkThemeReproducesLegacyLiterals(t *testing.T) {
	withTheme(t, "dark", theme.ProfileTrueColor)
	p := theme.Active().Palette

	assert.Equal(t, prompt.Green, promptColor(p.Info), "prefix was prompt.Green")
	assert.Equal(t, prompt.White, promptColor(p.TextStrong), "input text was prompt.White")
	assert.Equal(t, prompt.DarkGray, promptColor(p.Border), "suggestion background was prompt.DarkGray")
	assert.Equal(t, prompt.Black, promptColor(p.Background), "description background was prompt.Black")
	assert.Equal(t, prompt.DarkGray, promptColor(p.Muted), "scrollbar thumb was prompt.DarkGray")
}

// TestPromptColor_LightThemeIsNotWhite is the bug this bridge exists for: the
// input line was prompt.White under every theme, so a light theme on a light
// terminal printed white on white. It must now resolve to the palette's ink.
func TestPromptColor_LightThemeIsNotWhite(t *testing.T) {
	for _, name := range []string{"light", "solarized-light"} {
		t.Run(name, func(t *testing.T) {
			withTheme(t, name, theme.ProfileTrueColor)
			p := theme.Active().Palette

			got := promptColor(p.TextStrong)
			assert.NotEqual(t, prompt.White, got, "typed text must not stay white on a light theme")
			assert.Equal(t, prompt.Black, got, "the light palettes ink is index 0")
			assert.NotEqual(t, prompt.White, promptColor(p.Info), "the prefix must not be a bright hue either")
		})
	}
}

// TestPromptColor_NoColorProfileFallsBackToTerminal keeps piped/NO_COLOR runs
// in the terminal's own foreground instead of forcing a hue nothing else in
// the output is using.
func TestPromptColor_NoColorProfileFallsBackToTerminal(t *testing.T) {
	withTheme(t, "dark", theme.ProfileASCII)
	p := theme.Active().Palette
	assert.Equal(t, prompt.DefaultColor, promptColor(p.TextStrong))
	assert.Equal(t, prompt.DefaultColor, promptColor(p.Info))
}

// TestThemePromptOptions_CoverEveryThemeInRange proves the bridge yields a
// full, valid option set for every registered theme. go-prompt indexes its
// color enum directly, so an out-of-range value is a silent mis-paint (or a
// panic inside prompt.New at REPL start) — the worst possible place for it.
// prompt.New itself cannot run here: it opens the real TTY.
func TestThemePromptOptions_CoverEveryThemeInRange(t *testing.T) {
	for _, name := range theme.Names() {
		withTheme(t, name, theme.ProfileTrueColor)
		p := theme.Active().Palette

		assert.Len(t, themePromptTextOptions(), 3, name)
		assert.Len(t, themePromptColorOptions(), 13, name)

		for role, c := range map[string]theme.Color{
			"Info": p.Info, "TextStrong": p.TextStrong, "Border": p.Border,
			"Secondary": p.Secondary, "Background": p.Background,
			"Warning": p.Warning, "Muted": p.Muted,
		} {
			got := promptColor(c)
			assert.GreaterOrEqualf(t, int(got), int(prompt.DefaultColor), "%s/%s below the enum", name, role)
			assert.LessOrEqualf(t, int(got), int(prompt.White), "%s/%s above the enum", name, role)
		}
	}
}

// TestThemeDescriptions_ExistForEveryTheme is the guard for the defect that
// made `grafite` render the raw i18n key in the completer, the palette and
// `/config ui theme`: a palette was registered without its description string.
func TestThemeDescriptions_ExistForEveryTheme(t *testing.T) {
	i18n.Init()
	for _, name := range theme.Names() {
		key := "cfg.ui.theme_desc_" + name
		got := i18n.T(key)
		assert.NotEqualf(t, key, got, "theme %q has no description: %s renders as the raw key", name, key)
		assert.NotEmptyf(t, got, "theme %q has an empty description", name)
	}
}

// TestConfigUITheme_RebuildsThePromptOnlyInTheREPL locks the flag that makes a
// theme switch reach the input line. go-prompt froze its colors when the
// prompt was built, so without the rebuild `/config ui theme light` would
// leave the line being typed in the OLD theme's ink — white on white, the
// exact defect the switch was meant to cure. It must be raised ONLY while
// go-prompt owns the terminal: headless callers (scheduler, gateway,
// ACP/MCP, the agent and coder loops) have no prompt to rebuild, and a stuck
// flag there would end the next REPL prompt for no reason.
func TestConfigUITheme_RebuildsThePromptOnlyInTheREPL(t *testing.T) {
	i18n.Init()
	withTheme(t, "dark", theme.ProfileTrueColor)
	t.Setenv("CHATCLI_THEME", "dark")

	t.Run("inside the prompt it asks for a rebuild", func(t *testing.T) {
		c := &ChatCLI{promptRunning: true}
		c.configUITheme([]string{"light"})
		assert.True(t, c.themeReloadPending, "the REPL must rebuild its prompt")
		assert.Equal(t, "light", theme.Active().Name, "the switch itself must still have happened")
	})

	t.Run("headless it changes nothing but the theme", func(t *testing.T) {
		c := &ChatCLI{promptRunning: false}
		c.configUITheme([]string{"dracula"})
		assert.False(t, c.themeReloadPending, "there is no prompt to rebuild")
		assert.Equal(t, "dracula", theme.Active().Name)
	})

	t.Run("a no-op switch never rebuilds", func(t *testing.T) {
		require.NoError(t, theme.SetActive("nord"))
		c := &ChatCLI{promptRunning: true}
		c.configUITheme([]string{"nord"})
		assert.False(t, c.themeReloadPending)
	})

	t.Run("a rejected name never rebuilds", func(t *testing.T) {
		require.NoError(t, theme.SetActive("dark"))
		c := &ChatCLI{promptRunning: true}
		c.configUITheme([]string{"no-such-theme"})
		assert.False(t, c.themeReloadPending)
		assert.Equal(t, "dark", theme.Active().Name)
	})
}

// TestComposeOptions_FoldsAndShortCircuits covers the fold that lets a single
// entry in go-prompt's option list carry the whole theme wiring. Order and
// short-circuit matter: prompt.New PANICS on the first option that errors, so
// a fold that swallowed an error would move that panic to REPL start.
func TestComposeOptions_FoldsAndShortCircuits(t *testing.T) {
	var order []string
	ok := func(name string) prompt.Option {
		return func(*prompt.Prompt) error { order = append(order, name); return nil }
	}
	boom := errors.New("bad option")

	order = nil
	require.NoError(t, composeOptions(ok("a"), ok("b"), ok("c"))(nil))
	assert.Equal(t, []string{"a", "b", "c"}, order, "options apply in the order given")

	order = nil
	err := composeOptions(ok("a"), func(*prompt.Prompt) error { return boom }, ok("never"))(nil)
	require.ErrorIs(t, err, boom)
	assert.Equal(t, []string{"a"}, order, "the fold stops at the first failure")

	assert.NoError(t, composeOptions()(nil), "an empty fold is a no-op, not an error")
}

// TestThemePromptColorBundles_CarryEveryOption proves the two entry points the
// REPL and the coder prompt actually use carry the full set — a bundle that
// silently dropped an option would leave that surface on go-prompt's stock
// colors, which is where the white-on-white came from in the first place.
// The bundles are folds over these slices, and applying one to a Prompt is not
// possible here: go-prompt's renderer is unexported and prompt.New opens the
// real TTY. The counts are the contract; composeOptions is covered separately.
func TestThemePromptColorBundles_CarryEveryOption(t *testing.T) {
	withTheme(t, "light", theme.ProfileTrueColor)

	assert.Len(t, themePromptTextOptions(), 3, "prefix, input and preview")
	assert.Len(t, themePromptColorOptions(), 13, "input line plus the whole dropdown")
	assert.NotNil(t, themePromptTextColors(), "the coder prompt bundle must exist")
	assert.NotNil(t, themePromptColors(), "the chat REPL bundle must exist")
}

// TestConfigUISurfaces_RenderEveryThemeWithItsDescription drives the two
// listings a user reads to pick a theme. Both printed the description bare —
// so they never followed the palette — and both rendered the raw i18n key for
// any theme missing its string, which is exactly how grafite shipped.
func TestConfigUISurfaces_RenderEveryThemeWithItsDescription(t *testing.T) {
	i18n.Init()
	withTheme(t, "grafite", theme.ProfileTrueColor)

	for name, render := range map[string]func(*ChatCLI){
		"status": (*ChatCLI).printConfigUIStatus,
		"usage":  (*ChatCLI).printConfigUIUsage,
	} {
		t.Run(name, func(t *testing.T) {
			out := captureStdout(t, func() { render(&ChatCLI{}) })
			for _, theme := range theme.Names() {
				assert.Containsf(t, out, theme, "%s listing must offer %q", name, theme)
			}
			assert.NotContains(t, out, "cfg.ui.theme_desc_", "a raw i18n key reached the screen")
		})
	}
}
