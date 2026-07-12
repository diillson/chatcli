package cli

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/ui/theme"
)

// pinANSIProfile fixes the color profile so truncation assertions about the
// trailing reset are deterministic (kit.Truncate emits escapes only on
// colored profiles), restoring detection afterwards.
func pinANSIProfile(t *testing.T) {
	t.Helper()
	theme.SetProfile(theme.ProfileANSI)
	t.Cleanup(func() { theme.SetProfile(theme.DetectProfile()) })
}

func TestAnsiTruncate_ShortStringUnchanged(t *testing.T) {
	in := "\033[33mshort\033[0m line"
	if got := ansiTruncate(in, 40); got != in {
		t.Errorf("short input must pass through unchanged: got %q", got)
	}
}

func TestAnsiTruncate_ClampsVisibleWidth(t *testing.T) {
	pinANSIProfile(t)
	in := "\033[35m[servicenow/incidents]\033[0m " + strings.Repeat("x", 200)
	got := ansiTruncate(in, 40)

	if w := visibleLen(got); w > 40 {
		t.Errorf("visible width = %d, want <= 40", w)
	}
	if !strings.HasSuffix(got, "…"+ColorReset) {
		t.Errorf("truncated output must end with ellipsis + reset, got tail %q", got[len(got)-12:])
	}
	if !strings.Contains(got, "\033[35m") {
		t.Error("ANSI sequences inside the kept prefix must be preserved")
	}
}

func TestAnsiTruncate_WideRunesCountPerCell(t *testing.T) {
	pinANSIProfile(t)
	// Emoji and CJK occupy two terminal columns; byte- or rune-based
	// truncation would overflow the box and wrap the line.
	in := strings.Repeat("🚀", 30)
	got := ansiTruncate(in, 20)
	if w := visibleLen(got); w > 20 {
		t.Errorf("visible width = %d, want <= 20", w)
	}
	if strings.Contains(strings.TrimSuffix(got, "…"+ColorReset), "�") {
		t.Error("truncation must never split a rune")
	}
}

func TestUIBoxLine_NeverExceedsTerminalWidth(t *testing.T) {
	long := strings.Repeat("word ", 100)
	got := uiBoxLine(ColorPurple, long)
	// uiTermWidth delegates to kit.TermWidth, which falls back to 100
	// outside a TTY (the test environment) — the unified fallback.
	if w := visibleLen(got); w > uiTermWidth() {
		t.Errorf("box line visible width = %d, want <= %d", w, uiTermWidth())
	}
	if !strings.Contains(got, "│") {
		t.Error("box line must keep the sidebar prefix")
	}
}
