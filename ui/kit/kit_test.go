/*
 * ChatCLI - UI kit tests
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package kit

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/ui/theme"
	"github.com/mattn/go-runewidth"
)

// pinTheme fixes the theme and color profile so assertions are deterministic
// and TTY-independent, restoring detection afterwards.
func pinTheme(t *testing.T, p theme.Profile) {
	t.Helper()
	if err := theme.SetActive("dark"); err != nil {
		t.Fatalf("SetActive(dark): %v", err)
	}
	theme.SetProfile(p)
	t.Cleanup(func() { theme.SetProfile(theme.DetectProfile()) })
}

func TestRunewidthPinned(t *testing.T) {
	if runewidth.DefaultCondition.StrictEmojiNeutral {
		t.Fatal("StrictEmojiNeutral must be false: emoji must measure 2 cells or box borders drift")
	}
}

func TestColorizeProfiles(t *testing.T) {
	pinTheme(t, theme.ProfileANSI)
	got := Colorize("ok", theme.RoleToolSuccess)
	if !strings.Contains(got, "ok") || !strings.Contains(got, "\033[") || !strings.HasSuffix(got, "\033[0m") {
		t.Fatalf("ANSI profile: want colored span, got %q", got)
	}

	theme.SetProfile(theme.ProfileNoTTY)
	if got := Colorize("ok", theme.RoleToolSuccess); got != "ok" {
		t.Fatalf("NoTTY profile: want bare text, got %q", got)
	}
}

func TestBoldAndDim(t *testing.T) {
	pinTheme(t, theme.ProfileANSI)
	if got := Bold("Title", theme.RoleHeader); !strings.HasPrefix(got, "\033[1m") {
		t.Fatalf("Bold must lead with SGR 1, got %q", got)
	}
	if got := Dim("meta"); !strings.Contains(got, "meta") || !strings.Contains(got, "\033[") {
		t.Fatalf("Dim must color via RoleMuted, got %q", got)
	}

	theme.SetProfile(theme.ProfileNoTTY)
	if got := Bold("Title", theme.RoleHeader); got != "Title" {
		t.Fatalf("NoTTY Bold must be bare, got %q", got)
	}
}

func TestNoEscapesOnNoTTY(t *testing.T) {
	pinTheme(t, theme.ProfileNoTTY)
	outputs := []string{
		Notice(LevelSuccess, "done"),
		Notice(LevelError, "boom"),
		Rule(),
		RuleTitled("Section"),
		Header("Section"),
		Badge("default", theme.RoleMuted),
		KVTable([]KVRow{{Key: "Provider", Value: "CLAUDEAI"}}),
		List([]string{"a", "b"}),
		Numbered([]string{"a", "b"}),
		Sym(GlyphSuccess),
	}
	for i, out := range outputs {
		if strings.Contains(out, "\033") {
			t.Errorf("output %d contains escapes on NoTTY: %q", i, out)
		}
	}
}

func TestGlyphFallbackASCII(t *testing.T) {
	pinTheme(t, theme.ProfileASCII)
	pairs := map[Glyph]string{
		GlyphSuccess: "+", GlyphError: "x", GlyphWarn: "!",
		GlyphBullet: "-", GlyphArrow: ">", GlyphEllipsis: "...",
	}
	for g, want := range pairs {
		if got := g.String(); got != want {
			t.Errorf("glyph %d ASCII fallback = %q, want %q", g, got, want)
		}
	}

	theme.SetProfile(theme.ProfileTrueColor)
	if got := GlyphSuccess.String(); got != "✓" {
		t.Errorf("truecolor success glyph = %q, want ✓", got)
	}
}

func TestGlyphsAreSingleWidth(t *testing.T) {
	pinTheme(t, theme.ProfileTrueColor)
	for g := GlyphSuccess; g <= GlyphAssistant; g++ {
		if w := VisibleLen(g.String()); w != 1 {
			t.Errorf("glyph %d %q measures %d cells, want 1 (alignment contract)", g, g.String(), w)
		}
	}
}

func TestNoticeShape(t *testing.T) {
	pinTheme(t, theme.ProfileNoTTY)
	if got := Notice(LevelSuccess, "saved"); got != "  + saved" {
		t.Fatalf("Notice success = %q", got)
	}
	if got := Notice(LevelError, "failed"); got != "  x failed" {
		t.Fatalf("Notice error = %q", got)
	}
	theme.SetProfile(theme.ProfileANSI)
	if got := Notice(LevelWarn, "careful"); !strings.Contains(got, "careful") || !strings.Contains(got, "\033[") {
		t.Fatalf("warn message must be tinted, got %q", got)
	}
}

func TestRuleWidths(t *testing.T) {
	pinTheme(t, theme.ProfileNoTTY) // no TTY → fallback width, deterministic
	w := ContentWidth()
	if got := VisibleLen(Rule()); got != w {
		t.Fatalf("Rule width = %d, want %d", got, w)
	}
	for _, title := range []string{"Session", "Configuração de memória", "🚀 launch"} {
		if got := VisibleLen(RuleTitled(title)); got < w {
			t.Errorf("RuleTitled(%q) width = %d, want >= %d", title, got, w)
		}
	}
}

func TestKVTableAlignsTranslatedKeys(t *testing.T) {
	pinTheme(t, theme.ProfileNoTTY)
	rows := []KVRow{
		{Key: "Model", Value: "claude-fable-5"},
		{Key: "Custo da sessão", Value: "$0.004", Note: "(default)"},
		{Key: "Tokens", Value: "12k", ValueRole: theme.RoleStatus},
	}
	out := KVTable(rows)
	lines := strings.Split(out, "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 lines, got %d: %q", len(lines), out)
	}
	// Values must all start at the same column: indent(2) + widest key + gap(2).
	wantCol := 2 + VisibleLen("Custo da sessão") + 2
	for i, value := range []string{"claude-fable-5", "$0.004", "12k"} {
		idx := strings.Index(lines[i], value)
		if idx < 0 {
			t.Fatalf("value %q missing in %q", value, lines[i])
		}
		if col := VisibleLen(lines[i][:idx]); col != wantCol {
			t.Errorf("value %q starts at col %d, want %d (%q)", value, col, wantCol, lines[i])
		}
	}
	if !strings.Contains(out, "(default)") {
		t.Errorf("note missing: %q", out)
	}
}

func TestNumberedRightAligns(t *testing.T) {
	pinTheme(t, theme.ProfileNoTTY)
	items := make([]string, 10)
	for i := range items {
		items[i] = "item"
	}
	lines := strings.Split(Numbered(items), "\n")
	if lines[0] != "   1. item" || lines[9] != "  10. item" {
		t.Fatalf("numbers not right-aligned: %q vs %q", lines[0], lines[9])
	}
}

func TestTruncatePreservesANSIAndWidth(t *testing.T) {
	pinTheme(t, theme.ProfileANSI)
	colored := Colorize("abcdefghij", theme.RoleStatus)
	got := Truncate(colored, 6)
	if w := VisibleLen(got); w > 6 {
		t.Fatalf("truncated width %d > 6: %q", w, got)
	}
	if !strings.Contains(got, "…") {
		t.Fatalf("missing ellipsis: %q", got)
	}
	if !strings.Contains(got, "\033[") {
		t.Fatalf("ANSI prefix dropped: %q", got)
	}
	// Wide runes never split.
	if got := Truncate("日本語テスト", 5); VisibleLen(got) > 5 {
		t.Fatalf("CJK truncation overflows: %q (%d)", got, VisibleLen(got))
	}
	// Short input untouched.
	if got := Truncate("ok", 10); got != "ok" {
		t.Fatalf("short input mutated: %q", got)
	}
}

func TestStripVS16(t *testing.T) {
	in := "⚠️ atenção ▶️"
	out := StripVS16(in)
	if strings.Contains(out, "️") {
		t.Fatalf("VS16 survived: %q", out)
	}
	if !strings.Contains(out, "⚠") || !strings.Contains(out, "atenção") {
		t.Fatalf("content damaged: %q", out)
	}
}

func TestPadRightIsVisibleWidthAware(t *testing.T) {
	// "Custo da sessão" is 15 visible cols but 17 bytes — fmt's %-16s would
	// pad it short. PadRight must land both labels on the same column.
	a := PadRight("Custo da sessão", 20)
	b := PadRight("Model", 20)
	if VisibleLen(a) != 20 || VisibleLen(b) != 20 {
		t.Fatalf("PadRight widths: %d vs %d, want 20", VisibleLen(a), VisibleLen(b))
	}
	// At-or-past width: unchanged.
	if got := PadRight("longer-than-width", 5); got != "longer-than-width" {
		t.Fatalf("PadRight must not truncate: %q", got)
	}
	// ANSI-colored input: escapes don't count toward the width.
	theme.SetProfile(theme.ProfileANSI)
	t.Cleanup(func() { theme.SetProfile(theme.DetectProfile()) })
	colored := Colorize("key", theme.RoleStatus)
	if VisibleLen(PadRight(colored, 10)) != 10 {
		t.Fatalf("ANSI-aware padding failed: %q", PadRight(colored, 10))
	}
}

func TestBadge(t *testing.T) {
	pinTheme(t, theme.ProfileNoTTY)
	if got := Badge("api", theme.RoleMuted); got != "[api]" {
		t.Fatalf("Badge = %q", got)
	}
}

func TestContentWidthFloor(t *testing.T) {
	// Under test (piped), TermWidth falls back; ContentWidth must respect
	// margin and floor invariants regardless.
	w := ContentWidth()
	if w < MinContentWidth {
		t.Fatalf("ContentWidth %d below floor %d", w, MinContentWidth)
	}
	if TermWidth()-w > RightMargin && w != MinContentWidth {
		t.Fatalf("margin larger than RightMargin without hitting floor: term=%d content=%d", TermWidth(), w)
	}
}

// TestWarnGlyphUsesWarningHue guards the severity contract: the warning
// glyph must resolve to the palette's Warning color, not Info — the exact
// mismatch that motivated adding theme.RoleWarning.
func TestWarnGlyphUsesWarningHue(t *testing.T) {
	pinTheme(t, theme.ProfileTrueColor)
	warn := theme.Active().ColorFor(theme.RoleWarning)
	status := theme.Active().ColorFor(theme.RoleStatus)
	if warn == status {
		t.Skip("theme maps warning and info to the same color; nothing to distinguish")
	}
	got := Sym(GlyphWarn)
	if !strings.Contains(got, warn.SGR(theme.ProfileTrueColor)) {
		t.Fatalf("warn glyph not in Warning hue: %q", got)
	}
}
