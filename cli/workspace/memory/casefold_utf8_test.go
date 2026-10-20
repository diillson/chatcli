/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package memory

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// The old trim stepped back over continuation bytes and stopped on the lead
// byte, so a fact capped inside a multi-byte character kept half of it.
func TestSanitizeFactContent_CapKeepsUTF8Valid(t *testing.T) {
	content := strings.Repeat("a", maxFactContentChars-1) + "é" + " fim"
	got := SanitizeFactContent(content)
	if !utf8.ValidString(got) {
		t.Fatalf("capped fact is not valid UTF-8 (tail %q)", got[len(got)-6:])
	}
}

// The temporal span is found in the original query; an "İ" before it
// shifted the offset taken from a lowered copy.
func TestRemoveSpan_SurvivesCaseMappingLengthChanges(t *testing.T) {
	if got := removeSpan("İstanbul há 3 meses viagem", "há 3 meses"); got != "İstanbul viagem" {
		t.Errorf("removeSpan = %q, want %q", got, "İstanbul viagem")
	}
}

func TestParseEnhancedResponse_SectionsSurviveCaseMappingLengthChanges(t *testing.T) {
	daily, _, _, _, _, _ := parseEnhancedResponse("ıııı ſſ notas\n## DAILY\n- revisou o PR\n## LONGTERM\n- fato\n")
	if strings.TrimSpace(daily) != "- revisou o PR" {
		t.Errorf("daily = %q", daily)
	}
}
