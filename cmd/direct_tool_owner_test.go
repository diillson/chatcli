/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cmd

import (
	"context"
	"testing"

	"github.com/diillson/chatcli/cli"
	"github.com/diillson/chatcli/cli/agent/proc"
)

// A tool call that reaches the backend without a session never lands on
// the terminal's process table: it gets the table of sessionless callers.
// A call the surface marked keeps its session.
func TestDirectToolCallNeverUsesTheTerminalTable(t *testing.T) {
	if got := proc.OwnerFrom(directToolCtx(context.Background())); got != directToolOwner || got == "" {
		t.Fatalf("a sessionless direct call runs on %q, want its own table %q", got, directToolOwner)
	}
	marked := cli.WithProcSession(context.Background(), "s1")
	if got := proc.OwnerFrom(directToolCtx(marked)); got != proc.SessionOwner("s1") {
		t.Fatalf("a call with a session must keep it, got %q", got)
	}
}
