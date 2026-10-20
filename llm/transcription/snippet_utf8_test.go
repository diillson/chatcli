/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package transcription

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSnippet_CutKeepsUTF8Valid(t *testing.T) {
	body := strings.Repeat("a", maxErrorBodyToShow-1) + "é" + strings.Repeat("b", 50)
	got := snippet([]byte(body))
	if !utf8.ValidString(got) || !strings.HasSuffix(got, "…") {
		t.Fatalf("snippet = %q", got)
	}
	if short := snippet([]byte("  ok  ")); short != "ok" {
		t.Fatalf("short body = %q", short)
	}
}
