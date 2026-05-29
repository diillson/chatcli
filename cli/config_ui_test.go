/*
 * ChatCLI - tests for the /config ui theme mutator
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"os"
	"testing"

	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/ui/theme"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConfigUITheme_SwitchesActiveTheme verifies that the mutator flips the
// active theme and records the choice in the env (so a session reload keeps
// it), and that an invalid name is rejected without changing state.
func TestConfigUITheme_SwitchesActiveTheme(t *testing.T) {
	t.Cleanup(func() {
		_ = theme.SetActive("dark")
		_ = os.Unsetenv(config.ThemeEnv)
	})
	cli := &ChatCLI{}

	require.NoError(t, theme.SetActive("dark"))
	cli.configUITheme([]string{"light"})
	assert.Equal(t, "light", theme.Active().Name, "valid switch must change the active theme")
	assert.Equal(t, "light", os.Getenv(config.ThemeEnv), "switch must record the env for reload persistence")

	// Invalid theme: state unchanged.
	cli.configUITheme([]string{"chartreuse"})
	assert.Equal(t, "light", theme.Active().Name, "invalid theme must not change the active theme")
}

// TestRouteConfigUI_ThemeAlias confirms `/config theme <name>` resolves to the
// same path as `/config ui theme <name>`.
func TestRouteConfigUI_ThemeAlias(t *testing.T) {
	t.Cleanup(func() {
		_ = theme.SetActive("dark")
		_ = os.Unsetenv(config.ThemeEnv)
	})
	cli := &ChatCLI{}
	require.NoError(t, theme.SetActive("dark"))

	// Mirrors routeConfigCommand's "theme" case: args starts with "theme".
	cli.routeConfigUI([]string{"theme", "light"})
	assert.Equal(t, "light", theme.Active().Name)
}
