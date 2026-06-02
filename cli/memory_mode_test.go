/*
 * ChatCLI - tests for the memory injection mode loader.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import "testing"

func TestLoadMemoryMode(t *testing.T) {
	cases := map[string]string{
		"":         memModeIndex, // default is pull-first
		"index":    memModeIndex,
		"full":     memModeFull,
		"off":      memModeOff,
		"  FULL  ": memModeFull, // trimmed + case-insensitive
		"Index":    memModeIndex,
		"bogus":    memModeIndex, // unknown falls back to default
	}
	for in, want := range cases {
		t.Setenv("CHATCLI_MEMORY_MODE", in)
		if got := loadMemoryMode(); got != want {
			t.Errorf("loadMemoryMode(%q) = %q, want %q", in, got, want)
		}
	}
}
