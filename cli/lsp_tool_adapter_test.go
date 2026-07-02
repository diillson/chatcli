/*
 * ChatCLI - tests for the @lsp adapter's pure formatting helpers.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/diillson/chatcli/cli/agent/lsp"
)

func lspLoc(uri string, line, col int) lsp.Location {
	var l lsp.Location
	l.URI = uri
	l.Range.Start = lsp.Position{Line: line, Character: col}
	return l
}

func TestLSPRelPath(t *testing.T) {
	if got := relPath("/ws/proj", "file:///ws/proj/cli/cli.go"); got != "cli/cli.go" {
		t.Fatalf("in-root path = %q, want workspace-relative", got)
	}
	if got := relPath("/ws/proj", "file:///usr/lib/go/src/fmt/print.go"); got != "/usr/lib/go/src/fmt/print.go" {
		t.Fatalf("out-of-root path = %q, want absolute", got)
	}
	if got := relPath("", "file:///a/b.go"); got != "/a/b.go" {
		t.Fatalf("empty root = %q, want absolute", got)
	}
}

func TestLSPFormatLocationsBoundsAndConverts(t *testing.T) {
	locs := []lsp.Location{
		lspLoc("file:///ws/a.go", 9, 4),  // zero-based in
		lspLoc("file:///ws/b.go", 19, 0), // zero-based in
	}
	out := formatLocations("reference", "/ws", locs, 7)

	if !strings.Contains(out, "7 reference(s):") {
		t.Fatalf("missing total header: %q", out)
	}
	if !strings.Contains(out, "a.go:10:5") || !strings.Contains(out, "b.go:20:1") {
		t.Fatalf("positions not converted to 1-based: %q", out)
	}
	if !strings.Contains(out, "5 more not shown") {
		t.Fatalf("elision not flagged: %q", out)
	}

	full := formatLocations("definition", "/ws", locs[:1], 1)
	if strings.Contains(full, "more not shown") {
		t.Fatalf("no elision expected when all shown: %q", full)
	}
}

func TestLSPPosIsOneBasedInZeroBasedOut(t *testing.T) {
	p := lspPos(128, 14)
	if p.Line != 127 || p.Character != 13 {
		t.Fatalf("lspPos(128,14) = %+v, want zero-based 127/13", p)
	}
}

func TestTruncateHoverRuneSafe(t *testing.T) {
	if got := truncateHoverRuneSafe("short", 100); got != "short" {
		t.Fatalf("under-limit hover must pass through, got %q", got)
	}
	s := "a" + strings.Repeat("→", 800) // cut position lands mid-rune
	got := truncateHoverRuneSafe(s, 1000)
	if !utf8.ValidString(got) {
		t.Fatal("hover truncation produced invalid UTF-8")
	}
	if !strings.HasSuffix(got, "(hover truncated)") {
		t.Fatalf("missing truncation marker: %q", got[len(got)-30:])
	}
}
