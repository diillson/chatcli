//go:build !windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package main

import (
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"go.uber.org/zap"
)

// raiseSignal delivers sig to the current process. signal.Notify (installed by
// installSignalHandlers) captures it before the default disposition fires.
func raiseSignal(t *testing.T, sig syscall.Signal) {
	t.Helper()
	if err := syscall.Kill(os.Getpid(), sig); err != nil {
		t.Fatalf("kill(self, %v): %v", sig, err)
	}
}

// TestInstallSignalHandlers_SIGINTWhileExecuting is the regression test for the
// coder/agent re-entry bug: a Ctrl+C that arrives as a REAL SIGINT while an
// operation is in flight — e.g. at a cooked-mode security confirmation — must
// cancel ONLY the in-flight operation, never the root/session context. A root
// cancel there permanently poisoned every later coder/agent run (born from a
// cancelled context) while chat kept working on a fresh background context, and
// the only recovery was restarting the process.
func TestInstallSignalHandlers_SIGINTWhileExecuting(t *testing.T) {
	signal.Reset(syscall.SIGINT, syscall.SIGTERM)

	var rootCanceled atomic.Bool
	opCanceled := make(chan struct{}, 1)

	installSignalHandlers(
		func() bool { return true }, // operation in flight
		func() { opCanceled <- struct{}{} },
		func() { rootCanceled.Store(true) },
		zap.NewNop(),
	)

	raiseSignal(t, syscall.SIGINT)

	select {
	case <-opCanceled:
	case <-time.After(3 * time.Second):
		t.Fatal("SIGINT while executing did not cancel the operation")
	}

	// Give a wrong root-cancel a chance to fire before asserting it did not.
	time.Sleep(150 * time.Millisecond)
	if rootCanceled.Load() {
		t.Fatal("SIGINT while executing cancelled the ROOT context — this is the coder/agent re-entry bug")
	}
}

// TestInstallSignalHandlers_SIGINTWhileIdle confirms the other half: when no
// operation is running, Ctrl+C at the prompt is a shutdown request and cancels
// the root context so Start() returns and cleanup runs.
func TestInstallSignalHandlers_SIGINTWhileIdle(t *testing.T) {
	signal.Reset(syscall.SIGINT, syscall.SIGTERM)

	rootDone := make(chan struct{}, 1)

	installSignalHandlers(
		func() bool { return false }, // idle at the prompt
		func() { t.Error("operation cancel must not run when idle") },
		func() { rootDone <- struct{}{} },
		zap.NewNop(),
	)

	raiseSignal(t, syscall.SIGINT)

	select {
	case <-rootDone:
	case <-time.After(3 * time.Second):
		t.Fatal("idle SIGINT did not cancel the root context (shutdown regressed)")
	}
}

// TestInstallSignalHandlers_SIGTERMAlwaysShutsDown verifies SIGTERM cancels the
// root context even mid-operation — a kill is always a shutdown, never an
// operation interrupt.
func TestInstallSignalHandlers_SIGTERMAlwaysShutsDown(t *testing.T) {
	signal.Reset(syscall.SIGINT, syscall.SIGTERM)

	rootDone := make(chan struct{}, 1)

	installSignalHandlers(
		func() bool { return true }, // even while executing
		func() { t.Error("SIGTERM must not be treated as an operation cancel") },
		func() { rootDone <- struct{}{} },
		zap.NewNop(),
	)

	raiseSignal(t, syscall.SIGTERM)

	select {
	case <-rootDone:
	case <-time.After(3 * time.Second):
		t.Fatal("SIGTERM did not cancel the root context")
	}
}
