/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package devincli

import (
	"testing"
	"unicode/utf8"
)

func TestTail_KeepsUTF8Valid(t *testing.T) {
	if got := tail("abcéz", 2); got != "…z" || !utf8.ValidString(got) {
		t.Fatalf("tail = %q, want …z", got)
	}
	if got := tail("short", 10); got != "short" {
		t.Fatalf("tail = %q", got)
	}
}
