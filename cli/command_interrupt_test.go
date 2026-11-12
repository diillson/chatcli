/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"testing"
)

// Ctrl+C during a slash command used to begin shutting the session down,
// because nothing interruptible was registered while a handler ran.
func TestCtrlCCancelsTheCommandNotTheSession(t *testing.T) {
	c := &ChatCLI{}
	if c.CanInterrupt() {
		t.Fatal("idle prompt reports something to interrupt; Ctrl+C must quit there")
	}
	ctx, release := c.beginInterruptible(context.Background())
	if !c.CanInterrupt() {
		t.Fatal("a running command is not interruptible; Ctrl+C would quit the session")
	}
	// A nested registration (a command that itself holds the prompt)
	// shares the outer one and does not end it.
	inner, innerRelease := c.beginInterruptible(ctx)
	innerRelease()
	if !c.CanInterrupt() {
		t.Fatal("releasing a nested registration ended the outer one")
	}

	_ = captureStdout(t, c.CancelOperation) // first Ctrl+C
	if ctx.Err() == nil || inner.Err() == nil {
		t.Fatal("Ctrl+C did not cancel the command's context")
	}
	// A handler that ignores its context must not trap the user: the next
	// Ctrl+C finds nothing to interrupt and quits.
	if c.CanInterrupt() {
		t.Fatal("a command already interrupted still absorbs Ctrl+C")
	}
	release()

	// Work a handler started for later keeps its context after the
	// handler returns.
	ctx, release = c.beginInterruptible(context.Background())
	release()
	if ctx.Err() != nil {
		t.Fatal("returning from a command cancelled the context it handed to background work")
	}
	if c.CanInterrupt() {
		t.Fatal("registration outlived the command")
	}
	_ = captureStdout(t, c.CancelOperation)
	if ctx.Err() != nil {
		t.Fatal("Ctrl+C after the command returned reached its context")
	}
}
