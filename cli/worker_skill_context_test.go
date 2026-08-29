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

func TestWorkerSkillBlockNilSafe(t *testing.T) {
	a := &AgentMode{cli: &ChatCLI{}} // no personaHandler
	if got := a.workerSkillBlock("do the thing"); got != "" {
		t.Fatalf("no persona handler → empty, got %q", got)
	}
	if got := a.workerTaskContext("do the thing"); got != "" {
		t.Fatalf("no context → empty, got %q", got)
	}
}

func TestWorkerTaskContextJoinsBlocks(t *testing.T) {
	// A bare AgentMode's followUpRecallBlocks returns "" (no memory wired),
	// and workerSkillBlock returns "" (no persona) — the join is empty, not
	// a stray separator.
	a := &AgentMode{cli: &ChatCLI{}}
	if strings.TrimSpace(a.workerTaskContext("x")) != "" {
		t.Fatal("empty parts must join to empty, no stray newlines")
	}
}
