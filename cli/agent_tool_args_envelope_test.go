/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"strings"
	"testing"
)

// Regression: the model double-encoded the envelope as a JSON string and
// forgot the inner closing "}". The envelope used to be dropped silently, argv
// collapsed to ["write"] and the guard-rail answered "missing --file" — a
// misleading hint the model kept failing against on every retry.
func TestParseToolArgs_StringEnvelopeMissingClosingBrace(t *testing.T) {
	raw := `{"args":"{\"file\":\"/tmp/proj/PLAN.md\",\"encoding\":\"base64\",\"content\":\"IyBQbGFubwo=\"","cmd":"write"}`

	argv, err := parseToolArgsWithJSON(sanitizeToolCallArgs(raw, nil, "@coder", true))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if argv[0] != "write" {
		t.Fatalf("argv[0] = %q, want write", argv[0])
	}
	for flag, want := range map[string]string{
		"--file":     "/tmp/proj/PLAN.md",
		"--encoding": "base64",
		"--content":  "IyBQbGFubwo=",
	} {
		if got := findFlagValue(argv, flag); got != want {
			t.Errorf("%s = %q, want %q (argv=%q)", flag, got, want, argv)
		}
	}
	if missing, which := isCoderArgsMissingRequiredValue(argv); missing {
		t.Errorf("guard-rail reports missing %s for a recovered envelope", which)
	}
}

func TestParseToolArgs_TopLevelMissingClosingBrace(t *testing.T) {
	argv, err := parseToolArgsWithJSON(`{"cmd":"read","args":{"file":"main.go"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := findFlagValue(argv, "--file"); got != "main.go" {
		t.Errorf("--file = %q, want main.go (argv=%q)", got, argv)
	}
}

// A string envelope cut mid-value (truncated output) must never be completed
// into a "valid" call — that would write partial content as if it were whole.
// It must surface as a parse error naming the envelope, not as a silent drop.
func TestParseToolArgs_TruncatedStringEnvelopeIsAnError(t *testing.T) {
	raw := `{"cmd":"write","args":"{\"file\":\"/tmp/proj/PLAN.md\",\"content\":\"IyBQbG"}`

	argv, err := parseToolArgsWithJSON(raw)
	if err == nil {
		t.Fatalf("expected an error, got argv=%q", argv)
	}
	if !strings.Contains(err.Error(), `"args"`) {
		t.Errorf("error should name the envelope, got: %v", err)
	}
}

// Non-JSON string envelopes keep their previous handling (not an error here).
func TestParseToolArgs_PlainStringEnvelopeUnchanged(t *testing.T) {
	argv, err := parseToolArgsWithJSON(`{"cmd":"read","args":"main.go"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(argv) != 1 || argv[0] != "read" {
		t.Errorf("argv = %q, want [read]", argv)
	}
}
