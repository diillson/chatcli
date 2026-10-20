/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package server

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// With no break point in the window the chunker splits hard; the split must
// fall between characters, and a target narrower than one rune still moves.
func TestChunkResponse_HardSplitKeepsUTF8Valid(t *testing.T) {
	text := strings.Repeat("日本語", 40)
	for _, size := range []int{1, 2, 10, 64} {
		chunks := chunkResponse(text, size)
		if strings.Join(chunks, "") != text {
			t.Fatalf("size %d: chunks do not rebuild the text", size)
		}
		for _, c := range chunks {
			if !utf8.ValidString(c) {
				t.Fatalf("size %d: chunk %q splits a character", size, c)
			}
		}
	}
}
