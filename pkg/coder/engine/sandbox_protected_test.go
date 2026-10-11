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

// TestSandboxWritablePaths_ExcludeChatcliState: a confined command must not
// be able to write the policy, hooks, skills or plugins under ~/.chatcli.
func TestSandboxWritablePaths_ExcludeChatcliState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	state := home + "/.chatcli"
	for _, p := range sandboxWritablePaths("/work/ws") {
		if p == state || strings.HasPrefix(p, state+"/") {
			t.Fatalf("writable paths still include chatcli state: %q", p)
		}
	}
	protected := sandboxProtectedPaths()
	if len(protected) != 1 || protected[0] != state {
		t.Fatalf("protected paths = %v, want [%s]", protected, state)
	}
}
