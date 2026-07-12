/*
 * ChatCLI - UI kit geometry tests
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package kit

import (
	"strings"
	"testing"
)

func TestTitledTopBorderExactWidth(t *testing.T) {
	for _, tt := range []struct {
		header string
		width  int
	}{
		{"💬 RESPOSTA", 60},
		{"plain", 40},
		{"acentuação ção", 80},
	} {
		line := TitledTopBorder(tt.header, tt.width)
		if w := VisibleLen(line); w != tt.width {
			t.Errorf("TitledTopBorder(%q, %d) width = %d", tt.header, tt.width, w)
		}
		if !strings.HasPrefix(line, "╭── ") || !strings.HasSuffix(line, "╮") {
			t.Errorf("border shape broken: %q", line)
		}
	}
}

func TestTitledTopBorderOverflowFallback(t *testing.T) {
	line := TitledTopBorder("a very long header that cannot fit", 10)
	if !strings.Contains(line, "a very long header") || !strings.HasSuffix(line, "╮") {
		t.Errorf("overflow fallback must keep the header readable: %q", line)
	}
}

func TestBilateralBorderExactWidth(t *testing.T) {
	for _, tt := range []struct {
		left, right string
		width       int
	}{
		{" claude-fable-5 ", " 3.2s · 12k ", 70},
		{" only-left ", "", 50},
		{"", "", 44},
	} {
		line := BilateralBorder('╭', '╮', tt.left, tt.right, tt.width)
		if w := VisibleLen(line); w != tt.width {
			t.Errorf("BilateralBorder(%q,%q,%d) width = %d: %q", tt.left, tt.right, tt.width, w, line)
		}
	}
}

func TestBilateralBorderOverflowKeepsLabels(t *testing.T) {
	line := BilateralBorder('╰', '╯', " long left label ", " long right label ", 12)
	if !strings.Contains(line, "long left label") || !strings.Contains(line, "long right label") {
		t.Errorf("overflow fallback must keep labels: %q", line)
	}
}

func TestWrapStructuredKeepsRelativeIndent(t *testing.T) {
	in := "  root:\n    child: 1\n    wide: " + strings.Repeat("x", 120)
	out := WrapStructured(in, 40)
	if len(out) < 3 {
		t.Fatalf("expected wrapped lines, got %d", len(out))
	}
	// Common margin (2) dedented: root at col 0, child at col 2.
	if !strings.HasPrefix(out[0], "root:") {
		t.Errorf("common indent not dedented: %q", out[0])
	}
	if !strings.HasPrefix(out[1], "  child:") {
		t.Errorf("relative indent lost: %q", out[1])
	}
	for i, ln := range out {
		if w := VisibleLen(ln); w > 40 {
			t.Errorf("line %d exceeds limit: %d", i, w)
		}
	}
}

func TestStripANSIRemovesCSIOnly(t *testing.T) {
	in := "\x1b[38;5;140mkey\x1b[0m: value"
	if got := StripANSI(in); got != "key: value" {
		t.Errorf("StripANSI = %q", got)
	}
}

func TestTrimBlankBorderRowsKeepsMiddleBlanks(t *testing.T) {
	rows := []string{"", "\x1b[0m", "a", "", "b", ""}
	got := TrimBlankBorderRows(rows)
	want := []string{"a", "", "b"}
	if len(got) != len(want) {
		t.Fatalf("TrimBlankBorderRows = %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d = %q, want %q", i, got[i], want[i])
		}
	}
}
