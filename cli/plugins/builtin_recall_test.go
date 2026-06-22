/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import "testing"

func TestIsRecallTool(t *testing.T) {
	cases := map[string]bool{
		"@recall":  true,
		"recall":   true,
		"@RECALL":  true,
		" recall ": true,
		"@search":  false,
		"recalls":  false,
		"recal":    false,
		"":         false,
	}
	for in, want := range cases {
		if got := IsRecallTool(in); got != want {
			t.Errorf("IsRecallTool(%q) = %v, want %v", in, got, want)
		}
	}
}
