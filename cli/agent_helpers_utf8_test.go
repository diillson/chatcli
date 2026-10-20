/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestUITruncators_KeepUTF8Valid(t *testing.T) {
	s := strings.Repeat("a", 9) + "é" + strings.Repeat("b", 20)
	if got := truncateForUI(s, 10); !utf8.ValidString(got) || got != strings.Repeat("a", 9)+"..." {
		t.Errorf("truncateForUI = %q", got)
	}
	if got := compactText(s+"\nsecond line", 2, 10); !utf8.ValidString(got) || !strings.HasSuffix(got, "...") {
		t.Errorf("compactText = %q", got)
	}
}
