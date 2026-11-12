/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import "testing"

// The documented envelope {"cmd":"render","args":{"nodes":[…],"edges":[…]}}
// reaches the plugin flattened: one --nodes / --edges per element, each a
// JSON object. The argv branch read only source/theme/output/file, so the
// graph rendered with no nodes at all.
func TestGraphViewFlattenedEnvelopeKeepsTheGraph(t *testing.T) {
	argv := []string{
		"render",
		"--edges", `{"source":"a","target":"b"}`,
		"--nodes", `{"id":"a","label":"A"}`,
		"--nodes", `{"id":"b","label":"B"}`,
		"--title", "Deps",
	}
	got, err := parseGraphViewArgs(argv)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Nodes) != 2 || len(got.Edges) != 1 || got.Title != "Deps" {
		t.Fatalf("nodes=%d edges=%d title=%q", len(got.Nodes), len(got.Edges), got.Title)
	}
	// A single node arrives as one flag, not a list.
	if got, err = parseGraphViewArgs([]string{"render", "--nodes", `{"id":"only"}`}); err != nil || len(got.Nodes) != 1 {
		t.Fatalf("single node: %+v %v", got, err)
	}
	// The simple form without inline data is untouched.
	if _, ok := graphViewArgvToJSON([]string{"render", "--source", "knowledge", "--theme", "dark"}); ok {
		t.Fatal("a call with no inline graph was rebuilt as one")
	}
}
