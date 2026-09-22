/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	prompt "github.com/c-bata/go-prompt"
	"github.com/diillson/chatcli/cli/palette"
	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/diillson/chatcli/ui/theme"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

var dashURLPattern = regexp.MustCompile(`http://127\.0\.0\.1:\d+/\?t=[0-9a-f]{32}`)

func newDashCLI(t *testing.T) *ChatCLI {
	t.Helper()
	t.Setenv(pulseDashEnv, "")
	launched := 0
	prev := dashBrowserLauncher
	dashBrowserLauncher = func(string) error { launched++; return nil }
	c := &ChatCLI{logger: zap.NewNop()}
	c.initPulse(context.Background(), "repl")
	require.NotNil(t, c.pulse)
	t.Cleanup(func() {
		c.shutdownDash(context.Background())
		c.shutdownPulse()
		dashBrowserLauncher = prev
		assert.Zero(t, launched, "stdout is not a terminal under go test: the browser must never open")
	})
	return c
}

func TestDashOpenServesReusesAndStartsRecording(t *testing.T) {
	c := newDashCLI(t)
	assert.False(t, c.pulse.Recording(), "idle until a dashboard asks")

	out := captureStdout(t, func() { c.handleDashCommand(context.Background(), "/dash") })
	url := dashURLPattern.FindString(out)
	require.NotEmpty(t, url, "output must carry the loopback URL with its token: %q", out)

	again := captureStdout(t, func() { c.handleDashCommand(context.Background(), "/dash url") })
	assert.Equal(t, url, dashURLPattern.FindString(again), "one server per session, reused")

	require.Eventually(t, c.pulse.Recording, 3*time.Second, 10*time.Millisecond,
		"opening the dashboard takes the lease and this process records at once")

	resp, err := http.Get(strings.Split(url, "?")[0]) // #nosec G107 -- local test server
	require.NoError(t, err)
	page, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assert.Contains(t, string(page), `"self":"`+pulse.Default().Instance()+`"`)
	if theme.Active().Variant == theme.VariantLight {
		assert.Contains(t, string(page), `"--panel":"`+theme.Active().Palette.Background.Hex+`"`, "a light theme dresses the page")
	} else {
		assert.NotContains(t, string(page), `"--panel":"`, "a dark theme keeps the page's own terminal look")
	}

	status := captureStdout(t, func() { c.handleDashCommand(context.Background(), "/dash status") })
	assert.Contains(t, status, url)
	assert.Contains(t, status, "recording")
	assert.Contains(t, status, pulse.Default().Instance(), "this process is listed among the live ones")
}

func TestDashOffStopsServerAndRecording(t *testing.T) {
	c := newDashCLI(t)
	out := captureStdout(t, func() { c.handleDashCommand(context.Background(), "/dash open") })
	url := dashURLPattern.FindString(out)
	require.NotEmpty(t, url)
	require.Eventually(t, c.pulse.Recording, 3*time.Second, 10*time.Millisecond)

	stopped := captureStdout(t, func() { c.handleDashCommand(context.Background(), "/dash off") })
	assert.Contains(t, stopped, "stopped")
	require.Eventually(t, func() bool { return !c.pulse.Recording() }, 3*time.Second, 10*time.Millisecond,
		"releasing the lease makes this process go quiet without waiting for the TTL")
	down, err := http.Get(strings.Split(url, "?")[0]) // #nosec G107 -- local test server
	if down != nil {
		_ = down.Body.Close()
	}
	assert.Error(t, err, "the server must be down")

	idle := captureStdout(t, func() { c.handleDashCommand(context.Background(), "/dash off") })
	assert.Contains(t, idle, "not running")
	assert.Contains(t, captureStdout(t, func() { c.handleDashCommand(context.Background(), "/dash status") }), "not running")
}

func TestDashUsageAndUnavailable(t *testing.T) {
	c := newDashCLI(t)
	assert.Contains(t, captureStdout(t, func() { c.handleDashCommand(context.Background(), "/dash bogus") }), "/dash [open|url|status|off]")

	none := &ChatCLI{logger: zap.NewNop()} // no controller: no spool root
	assert.Contains(t, captureStdout(t, func() { none.handleDashCommand(context.Background(), "/dash") }), "unavailable")
	assert.False(t, none.shutdownDash(context.Background()))
	var nilCLI *ChatCLI
	assert.False(t, nilCLI.shutdownDash(context.Background()))
}

func TestRunDashForegroundServesUntilCancelled(t *testing.T) {
	prev := dashBrowserLauncher
	dashBrowserLauncher = func(string) error { t.Error("no terminal: must not launch"); return nil }
	t.Cleanup(func() { dashBrowserLauncher = prev })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunDashForeground(ctx) }()

	root, err := pulse.DefaultRoot()
	require.NoError(t, err)
	require.Eventually(t, func() bool { return pulse.LeaseActive(root, time.Now()) }, 3*time.Second, 10*time.Millisecond)
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("RunDashForeground did not return after cancel")
	}
	assert.False(t, pulse.LeaseActive(root, time.Now()), "shutdown releases the lease")
}

func TestDashThemeVarsFollowVariant(t *testing.T) {
	dark := dashThemeVars(theme.Theme{Variant: theme.VariantDark, Palette: theme.Palette{Background: theme.Color{Hex: "#202020"}, Text: theme.Color{Hex: "#eeeeee"}}})
	assert.Nil(t, dark, "dark: the page keeps its own terminal look, the deck's palette")

	light := dashThemeVars(theme.Theme{Variant: theme.VariantLight, Palette: theme.Palette{Background: theme.Color{Hex: "#e0e0e0"}, Muted: theme.Color{Hex: "#808080"}}})
	assert.Equal(t, "#e0e0e0", light["--panel"])
	assert.Equal(t, "#eeeeee", light["--bg"], "light: the ground sits above the panel")
	assert.Equal(t, "#d3d3d3", light["--panel2"], "light: the second surface sits just below the panel")

	for _, name := range theme.Names() {
		require.NoError(t, theme.SetActive(name))
		for k, v := range dashThemeVars(theme.Active()) {
			assert.Regexpf(t, `^#[0-9a-fA-F]{6}$`, v, "theme %s var %s", name, k)
		}
	}
}

func TestShadeHex(t *testing.T) {
	assert.Equal(t, "#000000", shadeHex("#808080", -1))
	assert.Equal(t, "#ffffff", shadeHex("#808080", 1))
	assert.Equal(t, "#808080", shadeHex("#808080", 0))
	assert.Equal(t, "nope", shadeHex("nope", 0.5))
	assert.Equal(t, "#zzzzzz", shadeHex("#zzzzzz", 0.5))
}

// Every page string must resolve in the catalog: a missing key would print
// the raw key on the dashboard.
func TestDashStringsAllTranslated(t *testing.T) {
	got := dashStrings()
	require.Len(t, got, len(dashStringKeys))
	for k, v := range got {
		assert.NotEmptyf(t, v, "dash.ui.%s", k)
		assert.NotEqualf(t, "dash.ui."+k, v, "dash.ui.%s is missing from the catalog", k)
	}
}

func TestDashIsWiredOnEverySurface(t *testing.T) {
	assert.True(t, isSideCommand("/dash"), "usable while a turn is running")
	assert.True(t, isSideCommand("/dash status"))
	assert.False(t, isSideCommand("/dashboard"), "word match, not raw prefix")

	found := false
	for _, rc := range palette.AllRootCommands() {
		if rc.Name == "/dash" {
			found = true
		}
	}
	assert.True(t, found, "command palette")

	c := &ChatCLI{}
	texts := func(s []prompt.Suggest) []string {
		out := make([]string, 0, len(s))
		for _, x := range s {
			out = append(out, x.Text)
		}
		return out
	}
	buf := prompt.NewBuffer()
	buf.InsertText("/dash ", false, true)
	assert.Equal(t, []string{"open", "url", "status", "off"}, texts(c.getDashSuggestions(*buf.Document())))
	buf = prompt.NewBuffer()
	buf.InsertText("/dash st", false, true)
	assert.Equal(t, []string{"status"}, texts(c.getDashSuggestions(*buf.Document())))
	buf = prompt.NewBuffer()
	buf.InsertText("/dash status extra", false, true)
	assert.Empty(t, c.getDashSuggestions(*buf.Document()))
}
