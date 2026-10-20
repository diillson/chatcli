/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package providerhost

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// The crash message quotes the end of the plugin's stderr; the cut must not
// start inside a multi-byte character.
func TestCrashDetail_TailKeepsUTF8Valid(t *testing.T) {
	p := &process{stderr: &tailBuffer{max: stderrTailBytes}}
	// "é" straddles the start of the last crashDetailBytes bytes.
	line := "x" + "é" + strings.Repeat("z", crashDetailBytes-1)
	_, _ = p.stderr.Write([]byte(line + "\n"))
	got := (&Host{}).crashDetail(p)
	if !utf8.ValidString(got) {
		t.Fatalf("crash detail is not valid UTF-8: %q", got)
	}
	if !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "zzz") {
		t.Fatalf("expected the elided tail, got %q", got)
	}
}
