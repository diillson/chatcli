/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/compress"
)

// newTestCompressionCLI builds a minimal ChatCLI carrying only a compression
// layer — enough to exercise compressionPluginAdapter without the full
// constructor.
func newTestCompressionCLI() *ChatCLI {
	return &ChatCLI{
		compressionLayer: compress.NewLayer(compress.Config{
			Mode:      compress.ModeLossyWithCCR,
			Store:     compress.NewMemoryStore(),
			Threshold: 100,
		}),
	}
}

func TestCompressionAdapterRoundTrip(t *testing.T) {
	a := &compressionPluginAdapter{cli: newTestCompressionCLI()}

	var sb strings.Builder
	for f := 0; f < 5; f++ {
		for ln := 1; ln <= 12; ln++ {
			fmt.Fprintf(&sb, "pkg/file%d.go:%d:some matching content row %d\n", f, ln, ln)
		}
	}
	original := sb.String()

	out, err := a.Compress("search", original)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) >= len(original) {
		t.Fatalf("expected reduction, got %d >= %d", len(out), len(original))
	}
	keys := compress.ExtractKeys(out)
	if len(keys) == 0 {
		t.Fatal("compressed output should carry a CCR marker")
	}
	// @recall must return the byte-identical original.
	got, ok := a.Recall(keys[0])
	if !ok || got != original {
		t.Fatal("Recall did not return the byte-identical original")
	}
}

func TestCompressionAdapterHintMapping(t *testing.T) {
	cases := map[string]compress.Hint{
		"search":   {ToolName: "@search"},
		"grep":     {ToolName: "@search"},
		"log":      {ToolName: "build"},
		"diff":     {ToolName: "git diff"},
		"code":     {MIME: "code"},
		"prose":    {MIME: "prose"},
		"markdown": {MIME: "prose"},
		"json":     {},
		"auto":     {},
		"":         {},
	}
	for in, want := range cases {
		if got := buildCompressionHint(in); got != want {
			t.Errorf("buildCompressionHint(%q) = %+v, want %+v", in, got, want)
		}
	}
}

func TestCompressionAdapterStatsNonEmpty(t *testing.T) {
	a := &compressionPluginAdapter{cli: newTestCompressionCLI()}
	s := a.Stats()
	if !strings.Contains(s, "Compression") || !strings.Contains(s, "mode=") {
		t.Fatalf("stats output missing expected fields: %q", s)
	}
}

func TestCompressionAdapterDisabledMode(t *testing.T) {
	cli := &ChatCLI{compressionLayer: compress.NewLayer(compress.Config{Mode: compress.ModeOff})}
	a := &compressionPluginAdapter{cli: cli}
	if _, err := a.Compress("log", strings.Repeat("x", 9000)); err == nil {
		t.Fatal("expected an error when compression is disabled")
	}
}
