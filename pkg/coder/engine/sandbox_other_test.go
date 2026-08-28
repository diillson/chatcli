//go:build !darwin && !linux

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

func TestWrapWithSandbox_UnsupportedDegrades(t *testing.T) {
	name, args, note := wrapWithSandbox(SandboxStrict, "/ws", "sh", "-c", "echo hi")
	if name != "sh" || len(args) != 2 || args[1] != "echo hi" {
		t.Fatalf("unsupported platform must run unconfined: %s %v", name, args)
	}
	if !strings.Contains(note, "not supported") {
		t.Fatalf("degrade note expected: %q", note)
	}
}
