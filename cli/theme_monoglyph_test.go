/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/coder"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/ui/theme"
)

func useTheme(t *testing.T, name string) {
	t.Helper()
	prev, prevProfile := theme.Active().Name, theme.ActiveProfile()
	if err := theme.SetActive(name); err != nil {
		t.Fatal(err)
	}
	theme.SetProfile(theme.ProfileTrueColor)
	t.Cleanup(func() {
		_ = theme.SetActive(prev)
		theme.SetProfile(prevProfile)
	})
}

// hasColoredEmoji reports whether s still carries a pictograph.
func hasColoredEmoji(s string) bool {
	for _, r := range s {
		if (r >= 0x1F000 && r <= 0x1FAFF) || (r >= 0x2600 && r <= 0x26FF) || r == '❌' || r == '✅' || r == 0xFE0F {
			return true
		}
	}
	return false
}

// Under a monochrome theme every central output path renders without
// colored emoji; under the default theme the same calls are untouched.
func TestOutputPathsFollowTheThemeGlyphs(t *testing.T) {
	const msg = "❌ build failed 📦"

	useTheme(t, "dark")
	if got := colorize(msg, ColorRed); !strings.Contains(got, msg) {
		t.Fatalf("the default theme changed the text: %q", got)
	}
	if got := uiBox("⚡", "HOOKS", ColorPurple); !strings.Contains(got, "⚡ HOOKS") {
		t.Fatalf("the default theme changed a box title: %q", got)
	}
	if got := i18n.T("cli.switch.unknown_arg", "x"); !strings.Contains(got, "❌") {
		t.Fatalf("the default theme changed a translated message: %q", got)
	}

	useTheme(t, "onyx")
	if got := colorize(msg, ColorRed); hasColoredEmoji(got) || !strings.Contains(got, "✗ build failed") {
		t.Fatalf("colorize under onyx: %q", got)
	}
	// The title keeps its text and loses the icon, whatever the layout
	// (the layout itself is covered in theme_layout_test.go).
	got := uiBox("⚡", "HOOKS", ColorPurple)
	if hasColoredEmoji(got) || !strings.Contains(got, "HOOKS") {
		t.Fatalf("a box title under onyx: %q", got)
	}
	if got := i18n.T("cli.switch.unknown_arg", "x"); hasColoredEmoji(got) || !strings.Contains(got, "✗") {
		t.Fatalf("a translated message under onyx: %q", got)
	}
}

// The permission prompt is drawn outside those paths, by the coder package.
func TestPermissionPromptFollowsTheThemeGlyphs(t *testing.T) {
	prompt := func() string {
		ch := make(chan string, 1)
		ch <- "n"
		return captureStdout(t, func() {
			_ = coder.PromptSecurityCheckWithContext(context.Background(), "@coder", `{"cmd":"exec","args":{"cmd":"make release"}}`, nil, ch)
		})
	}
	useTheme(t, "dark")
	if out := prompt(); !hasColoredEmoji(out) {
		t.Fatalf("the default theme lost the prompt's icons:\n%s", out)
	}
	useTheme(t, "onyx")
	out := prompt()
	if hasColoredEmoji(out) {
		t.Fatalf("the prompt still shows colored emoji under onyx:\n%s", out)
	}
	if !strings.Contains(out, "make release") {
		t.Fatalf("the prompt lost the command:\n%s", out)
	}
}

// The new themes are selectable by name like every other.
func TestOnyxIsSelectable(t *testing.T) {
	names := strings.Join(theme.Names(), ",")
	for _, want := range []string{"onyx", "onyx-light"} {
		if !strings.Contains(","+names+",", ","+want+",") {
			t.Errorf("%s is not a registered theme (%s)", want, names)
		}
	}
	useTheme(t, "onyx-light")
	if th := theme.Active(); th.Variant != theme.VariantLight || !th.Monoglyph {
		t.Fatalf("onyx-light: %+v", th)
	}
}
