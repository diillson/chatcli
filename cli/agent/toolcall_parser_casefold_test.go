/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package agent

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// "İ" lowercases to three bytes instead of two. The parser located tags in
// a strings.ToLower copy and cut the original at those offsets, so text
// with such characters before a call shifted every cut: the Raw span and
// the attributes came out wrong or split a character.
func TestParseToolCalls_OffsetsSurviveCaseMappingLengthChanges(t *testing.T) {
	call := `<tool_call name="@coder" args='{"cmd":"read","args":{"file":"main.go"}}' />`
	for _, prefix := range []string{"", "İstanbul İİİ — ", "KK "} {
		text := prefix + "Lendo o arquivo.\n" + call + "\nfim"
		calls, err := ParseToolCalls(text)
		if err != nil || len(calls) != 1 {
			t.Fatalf("prefix %q: calls=%v err=%v", prefix, calls, err)
		}
		c := calls[0]
		if c.Name != "@coder" || !strings.Contains(c.Args, `"main.go"`) || c.Raw != call {
			t.Errorf("prefix %q: got name=%q args=%q raw=%q", prefix, c.Name, c.Args, c.Raw)
		}
		if !utf8.ValidString(c.Raw) || !utf8.ValidString(c.Args) {
			t.Errorf("prefix %q: a cut split a character", prefix)
		}
	}
}
