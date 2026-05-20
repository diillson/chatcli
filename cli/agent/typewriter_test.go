/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package agent

import (
	"strings"
	"testing"
)

// TestTypewriterPrint_PreservesAllBytes guards the invariant that the
// typewriter helper is purely a pacing layer: every input rune must
// land on stdout in order, including ANSI color escapes and newlines.
// Any future "optimization" that drops or reorders bytes (e.g. a buffer
// that flushes on rune boundaries and loses partial UTF-8) trips here.
func TestTypewriterPrint_PreservesAllBytes(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"plain ascii", "hello world"},
		{"with newline", "line one\nline two\n"},
		{"ansi wrapped", "\x1b[32mgreen\x1b[0m and \x1b[31mred\x1b[0m"},
		{"multibyte runes", "olá, mundo — café ☕"},
		{"empty", ""},
		{"only ansi", "\x1b[1;35m\x1b[0m"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := captureStdout(t, func() {
				typewriterPrint(tc.input, 0)
			})
			if got != tc.input {
				t.Fatalf("typewriterPrint mangled bytes:\n  input:  %q\n  output: %q", tc.input, got)
			}
		})
	}
}

// TestRenderAssistantResponseTimelineEvent_TypesBodyContent guards the
// happy path of the assistant-response card: the typewriter variant
// must still produce the title, content, and a bottom border so the
// card visually closes. Char-by-char ordering is already covered by
// TestTypewriterPrint_PreservesAllBytes; here we only assert that
// content flows through and the card frame is intact.
func TestRenderAssistantResponseTimelineEvent_TypesBodyContent(t *testing.T) {
	r := NewUIRenderer(nil)
	out := captureStdout(t, func() {
		r.RenderAssistantResponseTimelineEvent("💬", "RESPOSTA", "Hello from the assistant.", ColorGray)
	})

	if !strings.Contains(out, "RESPOSTA") {
		t.Errorf("expected card title 'RESPOSTA' in output, got: %q", out)
	}
	if !strings.Contains(out, "Hello from the assistant.") {
		t.Errorf("expected body text in output, got: %q", out)
	}
	if !strings.Contains(out, "╰") {
		t.Errorf("expected bottom-border glyph in output (card must visually close), got: %q", out)
	}
}
