/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * Regression tests for the flag-form argv the agent flattener emits: each
 * case below is byte-for-byte what buildArgvFromJSONMap produces for the
 * plugin's own schema-taught JSON envelope (subcommand first, then sorted
 * "--key value" pairs; nested objects JSON-stringified; the "command" key
 * folded onto "cmd"). Parsers must read these, not treat flags as
 * positionals.
 */
package plugins

import (
	"strings"
	"testing"
)

func TestParseHTTPInvocation_FlagArgv(t *testing.T) {
	// {"cmd":"get","args":{"url":"https://api.github.com/zen"}}
	in, err := parseHTTPInvocation([]string{"get", "--url", "https://api.github.com/zen"})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if in.Method != "GET" || in.URL != "https://api.github.com/zen" {
		t.Fatalf("get parsed (%q,%q)", in.Method, in.URL)
	}

	// {"cmd":"request","args":{"method":"POST","url":"...","body":"{\"a\":1}","headers":{"X-Token":"t"},"timeout_seconds":30}}
	in, err = parseHTTPInvocation([]string{
		"request",
		"--body", `{"a":1}`,
		"--headers", `{"X-Token":"t"}`,
		"--method", "POST",
		"--timeout_seconds", "30",
		"--url", "https://api.example.com/v1",
	})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if in.Method != "POST" || in.URL != "https://api.example.com/v1" || in.Body != `{"a":1}` {
		t.Fatalf("request parsed (%q,%q,%q)", in.Method, in.URL, in.Body)
	}
	if in.Headers["X-Token"] != "t" {
		t.Fatalf("headers not decoded from stringified object: %+v", in.Headers)
	}
	if in.TimeoutSeconds != 30 {
		t.Fatalf("timeout_seconds = %d, want 30", in.TimeoutSeconds)
	}

	// Legacy positional forms must keep working.
	in, err = parseHTTPInvocation([]string{"get", "https://api.github.com/zen"})
	if err != nil || in.URL != "https://api.github.com/zen" {
		t.Fatalf("positional get: %v (%q)", err, in.URL)
	}
	in, err = parseHTTPInvocation([]string{"request", "GET", "https://api.github.com/zen"})
	if err != nil || in.Method != "GET" || in.URL != "https://api.github.com/zen" {
		t.Fatalf("positional request: %v (%q,%q)", err, in.Method, in.URL)
	}
}

func TestParseLSPInvocation_FlagArgv(t *testing.T) {
	// {"cmd":"definition","args":{"file":"cli/cli.go","line":128,"column":14}}
	cmd, inner, err := parseLSPInvocation([]string{"definition", "--column", "14", "--file", "cli/cli.go", "--line", "128"})
	if err != nil {
		t.Fatalf("definition: %v", err)
	}
	if cmd != "definition" {
		t.Fatalf("cmd = %q", cmd)
	}
	for _, want := range []string{`"file":"cli/cli.go"`, `"line":128`, `"column":14`} {
		if !strings.Contains(inner, want) {
			t.Fatalf("inner %q missing %s", inner, want)
		}
	}

	// Legacy positional form must keep working.
	_, inner, err = parseLSPInvocation([]string{"definition", "cli/cli.go", "128", "14"})
	if err != nil || !strings.Contains(inner, `"file":"cli/cli.go"`) || !strings.Contains(inner, `"line":128`) {
		t.Fatalf("positional: %v inner=%q", err, inner)
	}
}

func TestParseProcInvocation_FlagArgv(t *testing.T) {
	// {"cmd":"start","args":{"command":"npm run dev","dir":"./web"}} — the
	// flattener folds "command" onto "cmd", so argv carries --cmd.
	cmd, inner, err := parseProcInvocation([]string{"start", "--cmd", "npm run dev", "--dir", "./web"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if cmd != "start" || !strings.Contains(inner, `"command":"npm run dev"`) || !strings.Contains(inner, `"dir":"./web"`) {
		t.Fatalf("start inner = %q", inner)
	}

	// {"cmd":"status","args":{"id":"p1"}}
	cmd, inner, err = parseProcInvocation([]string{"status", "--id", "p1"})
	if err != nil || cmd != "status" || !strings.Contains(inner, `"id":"p1"`) {
		t.Fatalf("status: %v inner=%q", err, inner)
	}

	// Legacy positional forms must keep working.
	_, inner, err = parseProcInvocation([]string{"start", "npm", "run", "dev"})
	if err != nil || !strings.Contains(inner, `"command":"npm run dev"`) {
		t.Fatalf("positional start: %v inner=%q", err, inner)
	}
	_, inner, err = parseProcInvocation([]string{"logs", "p1"})
	if err != nil || !strings.Contains(inner, `"id":"p1"`) {
		t.Fatalf("positional logs: %v inner=%q", err, inner)
	}
}
