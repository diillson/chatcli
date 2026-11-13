/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package kit

import (
	"testing"

	"github.com/diillson/chatcli/ui/theme"
	"github.com/mattn/go-runewidth"
)

func withTheme(t *testing.T, name string) {
	t.Helper()
	prev := theme.Active().Name
	prevProfile := theme.ActiveProfile()
	if err := theme.SetActive(name); err != nil {
		t.Fatal(err)
	}
	theme.SetProfile(theme.ProfileTrueColor)
	t.Cleanup(func() {
		_ = theme.SetActive(prev)
		theme.SetProfile(prevProfile)
	})
}

func TestMonoglyph(t *testing.T) {
	withTheme(t, "onyx")
	cases := map[string]string{
		"plain ascii is untouched":           "plain ascii is untouched",
		"acentuação e ❯ setas → ficam":       "acentuação e ❯ setas → ficam",
		"❌ build failed":                     "✗ build failed",
		" ✅ saved":                           " ✓ saved",
		"⚠️ careful":                         "▲ careful",
		"⚠ careful":                          "▲ careful",
		"⏳ waiting":                          "↻ waiting",
		"🔔 job finished":                     "• job finished",
		"📦 Plugins":                          "Plugins",
		"🤖 Agent 🧠 thinking":                 "Agent thinking",
		"🕵️‍♂️ detective mode":               "detective mode",
		"upload ⬆ and download ⬇":            "upload ↑ and download ↓",
		"╭── ⚡ Hooks":                        "╭── Hooks",
		"table │ box ─ chars ● stay ◆ ✓ ✗ ↻": "table │ box ─ chars ● stay ◆ ✓ ✗ ↻",
		"": "",
	}
	for in, want := range cases {
		if got := Monoglyph(in); got != want {
			t.Errorf("Monoglyph(%q) = %q, want %q", in, got, want)
		}
	}
}

// Every glyph the rewrite produces is one cell wide, so columns and box
// borders that were measured before the rewrite still line up after it
// (they only get narrower where an emoji, two cells, was).
func TestMonoglyphOutputIsNarrow(t *testing.T) {
	withTheme(t, "onyx")
	for r := range monoStatus {
		out := Monoglyph(string(r))
		if w := runewidth.StringWidth(out); w != 1 {
			t.Errorf("%q became %q, %d cells wide", string(r), out, w)
		}
	}
	for r, plain := range monoArrows {
		if w := runewidth.StringWidth(plain); w != 1 {
			t.Errorf("%q became %q, %d cells wide", string(r), plain, w)
		}
	}
}

// Plain is a no-op unless the active theme asks for monochrome glyphs: the
// existing themes must look exactly as they did.
func TestPlainFollowsTheTheme(t *testing.T) {
	const s = "❌ failed 📦 pkg"
	for _, name := range theme.Names() {
		withTheme(t, name)
		got := Plain(s)
		if theme.Active().Monoglyph {
			if got != "✗ failed pkg" {
				t.Errorf("%s: Plain(%q) = %q", name, s, got)
			}
			continue
		}
		if got != s {
			t.Errorf("%s does not ask for monochrome glyphs but Plain changed %q to %q", name, s, got)
		}
	}
	withTheme(t, "onyx")
	if !theme.Active().Monoglyph {
		t.Fatal("onyx must ask for monochrome glyphs")
	}
	withTheme(t, "dark")
	if theme.Active().Monoglyph {
		t.Fatal("the default theme must not change its glyphs")
	}
}

func BenchmarkMonoglyphASCII(b *testing.B) {
	s := "  /jobs show 5ef3c1af  waiting  polling, 9m left"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = Monoglyph(s)
	}
}
