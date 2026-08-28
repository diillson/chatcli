//go:build darwin

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package engine

import (
	"strings"
	"testing"
)

func TestBuildSandboxProfile(t *testing.T) {
	ws := "/work/ws"

	work := buildSandboxProfile(SandboxWorkspace, ws)
	if !strings.Contains(work, "(allow default)") || !strings.Contains(work, "(deny file-write*)") {
		t.Fatalf("workspace profile missing baseline:\n%s", work)
	}
	if !strings.Contains(work, `(subpath "/work/ws")`) {
		t.Fatalf("workspace not in write allowlist:\n%s", work)
	}
	if strings.Contains(work, "(deny network*)") {
		t.Fatal("workspace mode must NOT deny network")
	}

	strict := buildSandboxProfile(SandboxStrict, ws)
	if !strings.Contains(strict, "(deny network*)") {
		t.Fatalf("strict profile must deny network:\n%s", strict)
	}
}

func TestWrapWithSandbox_DarwinShape(t *testing.T) {
	// The wrapper shape is asserted regardless of whether sandbox-exec is
	// on PATH: when absent it degrades, which is a different valid outcome,
	// so only assert structure when it wraps.
	name, args, note := wrapWithSandbox(SandboxStrict, "/ws", "sh", "-c", "echo hi")
	if name == sandboxBinary {
		if args[0] != "-p" || args[len(args)-3] != "sh" || args[len(args)-1] != "echo hi" {
			t.Fatalf("unexpected wrapped args: %v", args)
		}
		if !strings.Contains(note, "strict") {
			t.Fatalf("note should mention strict: %q", note)
		}
	}
}
