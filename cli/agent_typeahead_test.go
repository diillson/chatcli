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

	inline := formatTypeaheadPreviewInline("hello", 40)
	if !strings.Contains(inline, "❯ hello▌") {
		t.Errorf("inline preview must render prompt+cursor: %q", inline)
	}
	if formatTypeaheadPreviewInline("", 40) != "" {
		t.Error("empty inline preview must render nothing")
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
