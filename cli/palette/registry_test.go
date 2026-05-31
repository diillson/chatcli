/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package palette

import (
	"strings"
	"testing"
)

func TestRootCommandsAreWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range RootCommands() {
		if !strings.HasPrefix(c.Name, "/") {
			t.Errorf("command %q does not start with '/'", c.Name)
		}
		if seen[c.Name] {
			t.Errorf("duplicate command %q", c.Name)
		}
		seen[c.Name] = true
		if c.SummaryKey == "" {
			t.Errorf("command %q has empty SummaryKey", c.Name)
		}
		if _, ok := categoryKey[c.Category]; !ok {
			t.Errorf("command %q has category without label key", c.Name)
		}
	}
}

func TestEveryCategoryHasALabelKey(t *testing.T) {
	for c := CatCore; c <= CatSystem; c++ {
		if _, ok := categoryKey[c]; !ok {
			t.Errorf("category %d has no label key", c)
		}
	}
}

func TestRootItemsMirrorRegistry(t *testing.T) {
	if got, want := len(rootItems()), len(RootCommands()); got != want {
		t.Fatalf("rootItems = %d, RootCommands = %d", got, want)
	}
	for _, it := range rootItems() {
		if !it.hasCat {
			t.Errorf("root item %q missing category flag", it.text)
		}
	}
}
