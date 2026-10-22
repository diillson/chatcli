/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"encoding/json"
	"testing"
)

// Plain-text content in the XML protocol (no base64) must reach the engine
// byte-for-byte through the sanitizer and the envelope-to-argv conversion:
// newlines, tabs, quotes, literal backslashes, backslash-newline
// continuations (shell scripts) and backticks (Go raw strings).
func TestCoderWrite_PlainTextContentSurvivesSanitizerAndArgv(t *testing.T) {
	content := "#!/bin/sh\nset -e\ncurl -fsSL https://example.test \\\n  -o out.txt\n" +
		"re='\\d+'\n\tprintf \"%s\\n\" \"it's\" `date`\n"
	raw, _ := json.Marshal(map[string]any{"cmd": "write", "args": map[string]any{"file": "run.sh", "content": content}})

	argv, err := parseToolArgsWithJSON(sanitizeToolCallArgs(string(raw), nil, "@coder", true))
	if err != nil {
		t.Fatalf("argv: %v", err)
	}
	if argv[0] != "write" {
		t.Fatalf("argv[0] = %q, want write (argv=%q)", argv[0], argv)
	}
	if got := findFlagValue(argv, "--content"); got != content {
		t.Fatalf("content changed in transit:\n got %q\nwant %q", got, content)
	}
	if got := findFlagValue(argv, "--encoding"); got != "" {
		t.Fatalf("no encoding must be injected for plain text, got %q", got)
	}
}
