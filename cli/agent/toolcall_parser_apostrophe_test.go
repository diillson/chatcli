/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package agent

import (
	"encoding/json"
	"testing"
)

// Models write args='{"query":"Mary's"}' with the apostrophe unescaped. The
// first quote used to close the attribute: the JSON was cut at "Mary", the
// policy gate saw a broken command and blocked it, and every call after it
// in the reply was lost. Found by LongMemEval questions about "St. Mary's"
// and "Hitchhiker's Guide".
func TestParseToolCalls_ApostropheInsideSingleQuotedJSON(t *testing.T) {
	text := `<plan>look it up</plan>
<tool_call name="@session" args='{"cmd":"search","args":{"query":"Hitchhiker's Guide to the Galaxy started reading"}}' />
<tool_call name="@session" args='{"cmd":"get","args":{"name":"chat-1","query":"St. Mary's Church"}}' />
<tool name="@memory" args='{"cmd":"recall","args":{"query":"Rachel's wedding"}}' />`
	calls, err := ParseToolCalls(text)
	if err != nil || len(calls) != 3 {
		t.Fatalf("calls=%d err=%v: %+v", len(calls), err, calls)
	}
	want := []string{"Hitchhiker's Guide to the Galaxy started reading", "St. Mary's Church", "Rachel's wedding"}
	for i, c := range calls {
		var env struct {
			Args struct {
				Query string `json:"query"`
			} `json:"args"`
		}
		if err := json.Unmarshal([]byte(c.Args), &env); err != nil || env.Args.Query != want[i] {
			t.Errorf("call %d: args=%q (%v), want query %q", i, c.Args, err, want[i])
		}
	}
}

func TestParseToolCalls_SingleQuoteEdgeCases(t *testing.T) {
	cases := map[string]struct {
		text, args string
	}{
		// Recovered JSON keeps its source span, so the escaped form does not
		// also come back as a second, JSON-scanned call.
		"escaped apostrophe still works": {
			`<tool_call name="@session" args='{"cmd":"search","args":{"query":"Mary\'s"}}' />`,
			`{"cmd":"search","args":{"query":"Mary's"}}`,
		},
		"double-quoted args unchanged": {
			`<tool_call name="@session" args="{\"cmd\":\"list\"}" />`,
			`{"cmd":"list"}`,
		},
		"shell-style quoted apostrophe": {
			`<tool_call name="@session" args='{"cmd":"search","args":{"query":"Hitchhiker'"'"'s Guide"}}' />`,
			`{"cmd":"search","args":{"query":"Hitchhiker's Guide"}}`,
		},
		"non-JSON value keeps the first quote": {
			`<tool_call name="@coder" args='read main.go' />`,
			`read main.go`,
		},
	}
	for name, c := range cases {
		calls, err := ParseToolCalls(c.text)
		if err != nil || len(calls) != 1 || calls[0].Args != c.args {
			t.Errorf("%s: calls=%+v err=%v, want args %q", name, calls, err, c.args)
		}
	}
	// Unbalanced JSON falls back to the old first-quote rule instead of
	// swallowing the rest of the reply.
	calls, _ := ParseToolCalls(`<tool_call name="@session" args='{"cmd":"search"' /> trailing`)
	if len(calls) != 1 {
		t.Errorf("unbalanced JSON: %+v", calls)
	}
}
