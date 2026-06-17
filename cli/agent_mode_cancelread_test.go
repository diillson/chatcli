/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// TestReadLineSignal_ReturnsLineWhenAvailable pins the happy path: a line
// already queued on the centralized reader is returned verbatim, regardless of
// the cancel channel.
func TestReadLineSignal_ReturnsLineWhenAvailable(t *testing.T) {
	a := &AgentMode{cli: &ChatCLI{}}
	a.stdinLines = make(chan string, 1)
	a.stdinLines <- "sim, quero executar conscientemente"

	got := a.readLineSignal(nil)

	assert.Equal(t, "sim, quero executar conscientemente", got)
}

// TestReadLineSignal_AbortsOnCancel is the regression test for the core bug: a
// Ctrl+C cancels the operation, and a confirmation read blocked on the stdin
// channel must unblock immediately with "" instead of hanging until the user
// presses Enter. The cancel races an empty channel, so the only way out is the
// cancel arm.
func TestReadLineSignal_AbortsOnCancel(t *testing.T) {
	a := &AgentMode{cli: &ChatCLI{}}
	a.stdinLines = make(chan string) // unbuffered + never written: would hang without the fix

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan string, 1)
	go func() { done <- a.readLineSignal(ctx.Done()) }()

	// Give the goroutine a beat to block on the receive, then interrupt.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case got := <-done:
		assert.Equal(t, "", got, "cancelled confirmation must decline (empty) not hang")
	case <-time.After(2 * time.Second):
		t.Fatal("readLineSignal hung after cancellation — the Ctrl+C abort regressed")
	}
}

// TestReadLineSignal_AlreadyCancelled covers the edge where the signal is
// already fired before the read starts (e.g. a second confirmation after the
// first interrupt): it must still return "" without blocking.
func TestReadLineSignal_AlreadyCancelled(t *testing.T) {
	a := &AgentMode{cli: &ChatCLI{}}
	a.stdinLines = make(chan string)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan string, 1)
	go func() { done <- a.readLineSignal(ctx.Done()) }()

	select {
	case got := <-done:
		assert.Equal(t, "", got)
	case <-time.After(2 * time.Second):
		t.Fatal("readLineSignal hung on an already-cancelled signal")
	}
}

// TestReadLineSignal_NilChannelBlocksForInput verifies that with no operation in
// flight (nil cancel channel) the read waits for input rather than treating the
// nil channel as fired — a nil channel must select as "never".
func TestReadLineSignal_NilChannelBlocksForInput(t *testing.T) {
	a := &AgentMode{cli: &ChatCLI{}}
	a.stdinLines = make(chan string, 1)

	done := make(chan string, 1)
	go func() { done <- a.readLineSignal(nil) }()

	// Nothing queued yet: the read must still be blocking (not returned "").
	select {
	case got := <-done:
		t.Fatalf("nil cancel channel must not fire; read returned early with %q", got)
	case <-time.After(50 * time.Millisecond):
	}

	// Once a line arrives, it is delivered normally.
	a.stdinLines <- "ok"
	select {
	case got := <-done:
		assert.Equal(t, "ok", got)
	case <-time.After(2 * time.Second):
		t.Fatal("read did not deliver the line after it was queued")
	}
}

// TestReadLineSignal_UnattendedAutoApproves keeps the daemon contract:
// unattended runs have no human/stdin, so confirmations auto-approve regardless
// of the cancel channel.
func TestReadLineSignal_UnattendedAutoApproves(t *testing.T) {
	a := &AgentMode{cli: &ChatCLI{unattended: true}}

	got := a.readLineSignal(nil)

	assert.Equal(t, unattendedConfirmAnswer, got)
}

// TestReadLine_UsesActiveCancelSignal verifies the wiring: readLine() (the
// signature every confirmation site calls) resolves the active loop's cancel
// channel published by setCancelSignal, so cancelling that operation aborts the
// read.
func TestReadLine_UsesActiveCancelSignal(t *testing.T) {
	a := &AgentMode{cli: &ChatCLI{}}
	a.stdinLines = make(chan string)

	ctx, cancel := context.WithCancel(context.Background())
	a.setCancelSignal(ctx.Done())

	done := make(chan string, 1)
	go func() { done <- a.readLine() }()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case got := <-done:
		assert.Equal(t, "", got)
	case <-time.After(2 * time.Second):
		t.Fatal("readLine did not honor the active cancel signal")
	}
}

// TestCancelSignal_SaveRestore guarantees the re-entrant save/restore contract:
// the getter reflects whatever was last set, including detaching with nil.
func TestCancelSignal_SaveRestore(t *testing.T) {
	a := &AgentMode{cli: &ChatCLI{}}

	require.Nil(t, a.currentCancelSignal(), "no run set => nil signal")

	outer := make(chan struct{})
	a.setCancelSignal(outer)
	assert.Equal(t, (<-chan struct{})(outer), a.currentCancelSignal())

	a.setCancelSignal(nil)
	require.Nil(t, a.currentCancelSignal(), "detaching must restore the nil signal")
}

// TestRunWithCancellation_CancelledParentReturnsClean drives the wrapper with an
// already-cancelled parent context: the wrapped function must observe the
// cancelled operation context, the call must return within a deadline (never
// hang), and isExecuting must be reset so the REPL is usable again.
func TestRunWithCancellation_CancelledParentReturnsClean(t *testing.T) {
	cli := &ChatCLI{logger: zap.NewNop()}

	parent, cancel := context.WithCancel(context.Background())
	cancel() // operation is interrupted before the work even starts

	var sawCancelled bool
	done := make(chan struct{})
	go func() {
		cli.runWithCancellation(parent, "test", func(ctx context.Context) error {
			sawCancelled = ctx.Err() != nil
			return nil
		})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runWithCancellation hung on the cancelled path")
	}

	assert.True(t, sawCancelled, "wrapped fn should see the cancelled operation context")
	assert.False(t, cli.isExecuting.Load(), "isExecuting must be reset on return")
}
