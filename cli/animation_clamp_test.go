/*
 * ChatCLI - spinner single-line clamp tests.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"strings"
	"testing"
)

// TestClampSpinnerMessage pins the fix for the spinner line flood: a message
// wider than the terminal wrapped past the \r repaint and left one stale line
// behind per tick. The clamp guarantees a single visual row.
func TestClampSpinnerMessage(t *testing.T) {
	if got := clampSpinnerMessage("short", 80); got != "short" {
		t.Errorf("short message must pass through, got %q", got)
	}

	long := strings.Repeat("a", 500)
	got := clampSpinnerMessage(long, 80)
	if len([]rune(got)) > 80-8 {
		t.Errorf("clamped message = %d runes, must fit 80-col row with glyph margin", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("elision must be marked, got %q", got)
	}

	// Multibyte content truncates on rune boundaries, never mid-codepoint.
	acc := strings.Repeat("ção", 200)
	if got := clampSpinnerMessage(acc, 40); !strings.HasPrefix(got, "ção") || !strings.HasSuffix(got, "…") {
		t.Errorf("multibyte clamp broke runes: %q", got)
	}

	// Pathologically narrow terminals keep a usable floor.
	if got := clampSpinnerMessage(long, 10); len([]rune(got)) != 16 {
		t.Errorf("narrow-terminal floor = %d runes, want 16", len([]rune(got)))
	}
}
