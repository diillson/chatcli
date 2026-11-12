/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"reflect"
	"testing"
)

// The agent loop flattens a tool call's JSON envelope into argv before the
// plugin runs, so the argv form is what every tool actually receives. The
// plugin tests in cli/plugins/flattened_args_test.go start from these exact
// shapes; this test pins that they are what the loop produces. Keys come
// out sorted, a true boolean becomes a bare flag, a false one is dropped,
// and numbers become strings.
func TestToolCallFlatteningShapes(t *testing.T) {
	cases := []struct {
		call string
		want []string
	}{
		{`{"cmd":"send","args":{"to":"coder","text":"fix the build","card_id":"card-2"}}`,
			[]string{"send", "--card_id", "card-2", "--text", "fix the build", "--to", "coder"}},
		{`{"cmd":"list","args":{"channel":"telegram","limit":5}}`,
			[]string{"list", "--channel", "telegram", "--limit", "5"}},
		{`{"cmd":"search","args":{"q":"golang generics","max_results":3}}`,
			[]string{"search", "--max_results", "3", "--q", "golang generics"}},
		{`{"cmd":"apply","args":{"file":"a.go","patch":"p","dry_run":false}}`,
			[]string{"apply", "--file", "a.go", "--patch", "p"}},
		{`{"cmd":"apply","args":{"file":"a.go","patch":"p","apply":true}}`,
			[]string{"apply", "--apply", "--file", "a.go", "--patch", "p"}},
	}
	for _, c := range cases {
		got, err := parseToolArgsWithJSON(c.call)
		if err != nil {
			t.Fatalf("%s: %v", c.call, err)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s\n flattened to %q\n want         %q", c.call, got, c.want)
		}
	}
}
