/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"testing"
)

// findFlagValue returns the token following the given flag in argv, or "".
func findFlagValue(argv []string, flag string) string {
	for i, a := range argv {
		if a == flag && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return ""
}

// Regression for the field failure on Windows: the model emitted the args
// envelope double-encoded as a JSON STRING whose content carries unescaped
// Windows-path backslashes (invalid JSON escapes like \U). The flattener used
// to drop every argument ("falta --file"); it must recover the envelope.
func TestParseToolArgs_WindowsPathStringEnvelope(t *testing.T) {
	argLine := `{"args":"{\"file\":\"C:\\Users\\builder\\deployment.yaml\",\"content\":\"QQ==\",\"encoding\":\"base64\"}","cmd":"write"}`

	argv, err := parseToolArgsWithJSON(argLine)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(argv) == 0 || argv[0] != "write" {
		t.Fatalf("unexpected argv: %v", argv)
	}
	if got := findFlagValue(argv, "--file"); got != `C:\Users\builder\deployment.yaml` {
		t.Errorf("--file = %q, want the Windows path; argv=%v", got, argv)
	}
	if got := findFlagValue(argv, "--content"); got != "QQ==" {
		t.Errorf("--content = %q, want QQ==", got)
	}
}

// Same failure one level up: the whole args attribute arrives with raw
// single-backslash Windows paths, which is invalid JSON. The lenient repair
// pass must recover it instead of surfacing "Args parsing error".
func TestParseToolArgs_WindowsPathInvalidEscapesTopLevel(t *testing.T) {
	argLine := `{"cmd":"write","args":{"file":"C:\Users\builder\deployment.yaml","content":"QQ==","encoding":"base64"}}`

	argv, err := parseToolArgsWithJSON(argLine)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(argv) == 0 || argv[0] != "write" {
		t.Fatalf("unexpected argv: %v", argv)
	}
	if got := findFlagValue(argv, "--file"); got != `C:\Users\builder\deployment.yaml` {
		t.Errorf("--file = %q, want the Windows path; argv=%v", got, argv)
	}
}

// The guard-rail that fired in the field ("falta --file") must accept the
// recovered argv end-to-end.
func TestCoderArgsGuardRail_WindowsPathStringEnvelope(t *testing.T) {
	argLine := `{"args":"{\"file\":\"C:\\Users\\builder\\deployment.yaml\",\"content\":\"QQ==\",\"encoding\":\"base64\"}","cmd":"write"}`
	argv, err := parseToolArgsWithJSON(argLine)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if missing, which := isCoderArgsMissingRequiredValue(argv); missing {
		t.Errorf("guard-rail flagged %q as missing; argv=%v", which, argv)
	}
}
