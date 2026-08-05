/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cmd

import (
	"reflect"
	"testing"
)

// TestParseToolInvocation pins the CLI contract of `chatcli tool`:
// no args or "list" lists the catalog; a JSON envelope as the single
// argument passes through as a raw string; anything else is argv passed
// verbatim (never joined and re-split — see the @model parser lesson).
func TestParseToolInvocation(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want toolInvocation
	}{
		{"no args lists", nil, toolInvocation{List: true}},
		{"explicit list", []string{"list"}, toolInvocation{List: true}},
		{"argv form", []string{"@docs-flatten", "--root", ".", "--format", "jsonl"},
			toolInvocation{Name: "@docs-flatten", Argv: []string{"--root", ".", "--format", "jsonl"}}},
		{"name without at", []string{"wikipedia", "--term", "Go language"},
			toolInvocation{Name: "wikipedia", Argv: []string{"--term", "Go language"}}},
		{"json envelope", []string{"docs-flatten", `{"root":".","format":"jsonl"}`},
			toolInvocation{Name: "docs-flatten", JSON: `{"root":".","format":"jsonl"}`}},
		{"bare tool no args", []string{"@version"},
			toolInvocation{Name: "@version"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseToolInvocation(tc.args)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parseToolInvocation(%v) = %+v, want %+v", tc.args, got, tc.want)
			}
		})
	}
}
