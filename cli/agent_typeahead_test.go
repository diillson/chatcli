/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"strings"
	"testing"
)

// TestSplitStdinChunkBackspace pins the cbreak-mode line editing: without
// kernel canonical processing the reader must apply backspace itself,
// rune-safe for multi-byte input.
func TestSplitStdinChunkBackspace(t *testing.T) {
	var buf strings.Builder
	lines := splitStdinChunk([]byte("abd\x7fc\n"), &buf)
	if len(lines) != 1 || lines[0] != "abc\n" {
		t.Errorf("DEL must erase the previous byte: got %q", lines)
	}

	buf.Reset()
	lines = splitStdinChunk([]byte("olá\x7f\x7fi\n"), &buf)
	if len(lines) != 1 || lines[0] != "oi\n" {
		t.Errorf("backspace must be rune-safe over UTF-8: got %q", lines)
	}

	buf.Reset()
	lines = splitStdinChunk([]byte("\x7f\x08x\n"), &buf)
	if len(lines) != 1 || lines[0] != "x\n" {
		t.Errorf("backspace on an empty buffer must be a no-op: got %q", lines)
	}

	// Partial chunk keeps the edited residue for the preview.
	buf.Reset()
	if got := splitStdinChunk([]byte("/boarf\x7fd"), &buf); len(got) != 0 {
		t.Fatalf("no newline, no lines: got %q", got)
	}
	if buf.String() != "/board" {
		t.Errorf("residual partial must reflect backspace edits, got %q", buf.String())
	}
}

// TestFormatTypeaheadPreview pins the display contract: tail-biased
// truncation, control-byte stripping and empty-input silence.
func TestFormatTypeaheadPreview(t *testing.T) {
	if formatTypeaheadPreviewLine("") != "" {
		t.Error("empty preview must render nothing")
	}
	if formatTypeaheadPreviewLine("\x1b[A\x1b[B") != "" {
		t.Error("control-only preview must render nothing")
	}

	line := formatTypeaheadPreviewLine("/mail send coder foca no login")
	if !strings.Contains(line, "❯ /mail send coder foca no login▌") {
		t.Errorf("preview must show the typed text with prompt and cursor: %q", line)
	}
	if !strings.Contains(line, "\033[K") {
		t.Error("preview must clear to end of line so backspace never leaves residue")
	}

	long := strings.Repeat("a", 100) + "FIM"
	tail := formatTypeaheadPreviewLine(long)
	if !strings.Contains(tail, "…") || !strings.Contains(tail, "FIM▌") {
		t.Errorf("long input must show the tail with ellipsis: %q", tail)
	}

}

// TestFormatTypeaheadPreviewBelow pins the below-the-spinner placement: the
// suffix paints the line under the spinner and restores the cursor; when
// typing stops it wipes the stale line exactly once, and a quiet spinner
// never touches the row below (which would scroll at the terminal's
// bottom row).
func TestFormatTypeaheadPreviewBelow(t *testing.T) {
	suffix, had := formatTypeaheadPreviewBelow("/board", false)
	if !had {
		t.Fatal("non-empty preview must report hadPreview=true")
	}
	if !strings.HasPrefix(suffix, "\n") || !strings.HasSuffix(suffix, "\033[A\r") {
		t.Errorf("suffix must go down, paint and come back up: %q", suffix)
	}
	if !strings.Contains(suffix, "❯ /board▌") {
		t.Errorf("suffix must carry the preview line: %q", suffix)
	}

	wipe, had := formatTypeaheadPreviewBelow("", true)
	if had {
		t.Fatal("cleared preview must report hadPreview=false")
	}
	if wipe != "\n\033[K\033[A\r" {
		t.Errorf("transition to empty must wipe the stale line once: %q", wipe)
	}

	quiet, had := formatTypeaheadPreviewBelow("", false)
	if quiet != "" || had {
		t.Errorf("quiet spinner must not touch the row below, got %q", quiet)
	}
}

// TestTypeaheadPreviewSnapshot pins the reader→display handoff.
func TestTypeaheadPreviewSnapshot(t *testing.T) {
	a := &AgentMode{}
	if a.typeaheadPreviewSnapshot() != "" {
		t.Error("zero value must be empty")
	}
	a.setTypeaheadPreview("/agents")
	if got := a.typeaheadPreviewSnapshot(); got != "/agents" {
		t.Errorf("snapshot = %q", got)
	}
}
