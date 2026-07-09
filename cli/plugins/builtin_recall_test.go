/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"context"
	"strings"
	"testing"
)

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

func TestExtractCCRKeys_ToleratesModelSyntaxVariants(t *testing.T) {
	const key = "aaaabbbbccccdddd"
	cases := map[string]string{
		"canonical marker in JSON":     `{"key":"<<ccr:aaaabbbbccccdddd>>"}`,
		"bare key in JSON":             `{"key":"aaaabbbbccccdddd"}`,
		"ccr: prefix without brackets": `{"key":"ccr:aaaabbbbccccdddd"}`,
		"single angle brackets":        `{"key":"<ccr:aaaabbbbccccdddd>"}`,
		"uppercase hex":                `{"key":"CCR:AAAABBBBCCCCDDDD"}`,
		"spacing inside marker":        `{"key":"<< ccr: aaaabbbbccccdddd >>"}`,
		"marker field alias":           `{"marker":"<<ccr:aaaabbbbccccdddd>>"}`,
		"id field alias":               `{"id":"aaaabbbbccccdddd"}`,
		"backticked key":               "{\"key\":\"`aaaabbbbccccdddd`\"}",
		"bare payload, no JSON":        `ccr:aaaabbbbccccdddd`,
		"naked hex payload":            `aaaabbbbccccdddd`,
		"marker buried in prose":       `{"key":"please recall <<ccr:aaaabbbbccccdddd>> for me"}`,
		"wrong field, marker in raw":   `{"target":"<<ccr:aaaabbbbccccdddd>>"}`,
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			keys := extractCCRKeys(payload)
			if len(keys) != 1 || keys[0] != key {
				t.Errorf("extractCCRKeys(%q) = %v, want [%s]", payload, keys, key)
			}
		})
	}
}

func TestExtractCCRKeys_Negatives(t *testing.T) {
	for name, payload := range map[string]string{
		"empty":              ``,
		"fifteen hex chars":  `{"key":"aaaabbbbccccddd"}`,
		"non-hex":            `{"key":"not-a-ccr-key-at-all"}`,
		"naked hex in prose": `the hash aaaabbbbccccdddd appeared in the log`, // no ccr anchor, not exact
	} {
		t.Run(name, func(t *testing.T) {
			if keys := extractCCRKeys(payload); len(keys) != 0 {
				t.Errorf("extractCCRKeys(%q) = %v, want none", payload, keys)
			}
		})
	}
}

func TestRecall_CCRPrefixWithoutBrackets(t *testing.T) {
	// Regression for the exact failure reported in the field: the model
	// pastes "ccr:KEY" (no angle brackets) and recall must still resolve it.
	defer withAdapter(&fakeCompressionAdapter{
		store: map[string]string{"aaaabbbbccccdddd": "the original content"},
	})()

	p := NewBuiltinRecallPlugin()
	out, err := p.Execute(context.Background(), []string{`{"key":"ccr:aaaabbbbccccdddd"}`})
	if err != nil {
		t.Fatalf("recall with ccr: prefix must succeed, got: %v", err)
	}
	if out != "the original content" {
		t.Errorf("single-key recall must return the original verbatim, got %q", out)
	}
}

func TestRecall_MultipleMarkersLabeledSections(t *testing.T) {
	defer withAdapter(&fakeCompressionAdapter{
		store: map[string]string{
			"aaaaaaaaaaaaaaaa": "first original",
			"bbbbbbbbbbbbbbbb": "second original",
		},
	})()

	p := NewBuiltinRecallPlugin()
	out, err := p.Execute(context.Background(),
		[]string{`{"key":"<<ccr:aaaaaaaaaaaaaaaa>> and <<ccr:cccccccccccccccc>> and <<ccr:bbbbbbbbbbbbbbbb>>"}`})
	if err != nil {
		t.Fatalf("multi-key recall with partial hits must succeed, got: %v", err)
	}
	for _, want := range []string{
		"--- @recall <<ccr:aaaaaaaaaaaaaaaa>> ---", "first original",
		"--- @recall <<ccr:bbbbbbbbbbbbbbbb>> ---", "second original",
		"[not found — expired or evicted from the local store]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("multi-key output missing %q in:\n%s", want, out)
		}
	}
}

func TestRecall_NoKeyErrorIsActionable(t *testing.T) {
	defer withAdapter(&fakeCompressionAdapter{})()

	p := NewBuiltinRecallPlugin()
	_, err := p.Execute(context.Background(), []string{`{"key":"garbage"}`})
	if err == nil || !strings.Contains(err.Error(), "16 hex chars") {
		t.Errorf("error must teach the expected key format, got: %v", err)
	}
}
