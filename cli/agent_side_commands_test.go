/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestIsSideCommand pins the mid-run allowlist: observation commands match
// (bare and with arguments), everything else — especially the panic-sentinel
// mode switches and plain text — does not.
func TestIsSideCommand(t *testing.T) {
	for line, want := range map[string]bool{
		"/agents":                        true,
		"/agents cancel run-a-1":         true,
		"/board":                         true,
		"/board move card-1 doing":       true,
		"/mail send coder foca no login": true,
		"/jobs":                          true,
		"/jobs list":                     true,
		"/agentsx":                       false,
		"/boardgame":                     false,
		"/agent fix x":                   false, // mode switch — panics a sentinel, never allowed
		"/coder":                         false,
		"/run x":                         false,
		"/exit":                          false,
		"faz o deploy":                   false,
		"":                               false,
	} {
		if got := isSideCommand(line); got != want {
			t.Errorf("isSideCommand(%q) = %v, want %v", line, got, want)
		}
	}
}

// TestOnSideCommandQueuesWithoutLiveDisplay pins the fallback path: with no
// live display (nil timer), the command queues and applySideCommands later
// executes it exactly once, in order.
func TestOnSideCommandQueuesWithoutLiveDisplay(t *testing.T) {
	var executed []string
	a := &AgentMode{sideCmdExec: func(line string) { executed = append(executed, line) }}

	ctx := context.Background()
	a.onSideCommand(ctx, "/board")
	a.onSideCommand(ctx, "/agents")
	if len(executed) != 0 {
		t.Fatalf("commands must queue while no display is live, got %v", executed)
	}

	a.applySideCommands(ctx)
	if len(executed) != 2 || executed[0] != "/board" || executed[1] != "/agents" {
		t.Errorf("queued commands must apply in order, got %v", executed)
	}

	a.applySideCommands(ctx)
	if len(executed) != 2 {
		t.Error("applySideCommands must drain the queue (no re-execution)")
	}
}

// TestStdinReaderRefcount pins the re-entrancy contract with the reader
// lifecycle stubbed at the field level: an inner start/stop pair must not
// tear down the outer scope's reader, and resume only respawns while a
// scope still holds a reference.
func TestStdinReaderRefcount(t *testing.T) {
	a := &AgentMode{}
	// Simulate an already-running reader owned by an outer scope without
	// spawning a real goroutine: hand-set the fields under the same lock
	// discipline the real spawn uses.
	a.stdinMu.Lock()
	a.stdinDepth = 1
	a.stdinLines = make(chan string, 1)
	fakeDone := make(chan struct{})
	a.stdinDone = fakeDone
	a.stdinMu.Unlock()

	// Nested entry: start must reuse, not respawn.
	a.startStdinReader(context.Background())
	if a.stdinDone != fakeDone {
		t.Fatal("nested start must reuse the outer reader")
	}
	// Inner stop: depth 2→1, reader must survive.
	a.stopStdinReader()
	if a.stdinLines == nil || a.stdinDone == nil {
		t.Fatal("inner stop must not tear down the outer scope's reader")
	}
	// Outer stop: depth 1→0, teardown (closes fakeDone, no goroutine to wait).
	a.stopStdinReader()
	if a.stdinLines != nil || a.stdinDone != nil {
		t.Fatal("final stop must tear the reader down")
	}
	select {
	case <-fakeDone:
	default:
		t.Fatal("teardown must close the done channel")
	}
	// Resume after the last stop must NOT leak a reader into the REPL.
	a.resumeStdinReader(context.Background())
	if a.stdinLines != nil {
		t.Fatal("resume with zero depth must not respawn the reader")
	}
}

// TestSideCommandRootsRouteSafely guards the allowlist against drift: every
// root must be a word-route in the command handler that neither panics nor
// requests exit. Uses the router's routing table shape indirectly — the
// roots must all be prefixes the handler recognizes.
func TestSideCommandRootsRouteSafely(t *testing.T) {
	for _, root := range sideCommandRoots {
		if !strings.HasPrefix(root, "/") {
			t.Errorf("side command root %q must be a slash command", root)
		}
		switch root {
		case "/agent", "/coder", "/run", "/plan", "/exit":
			t.Errorf("mode-switch command %q must never be side-runnable", root)
		}
	}
}

// TestOnSideCommandExecutesImmediatelyUnderFreeGate pins the streaming-run
// responsiveness fix: with no live display but a free prompt gate, the
// command runs NOW (serialized with stream output); with the gate held by a
// security prompt, it queues — and never blocks the caller, because this
// path runs on the stdin reader goroutine the prompt depends on.
func TestOnSideCommandExecutesImmediatelyUnderFreeGate(t *testing.T) {
	var executed []string
	pa := &workerPolicyAdapter{}
	a := &AgentMode{
		policyAdapter: pa,
		sideCmdExec:   func(line string) { executed = append(executed, line) },
	}
	ctx := context.Background()

	a.onSideCommand(ctx, "/taskgraph status")
	if len(executed) != 1 || executed[0] != "/taskgraph status" {
		t.Fatalf("free gate must execute immediately, got %v", executed)
	}

	// Gate held (a prompt on screen): must queue without blocking.
	pa.mu.Lock()
	done := make(chan struct{})
	go func() {
		a.onSideCommand(ctx, "/agents")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("onSideCommand must never block on a held prompt gate")
	}
	pa.mu.Unlock()
	if len(executed) != 1 {
		t.Fatalf("held gate must queue, not execute, got %v", executed)
	}
	a.applySideCommands(ctx)
	if len(executed) != 2 || executed[1] != "/agents" {
		t.Fatalf("queued command must run at the boundary, got %v", executed)
	}
}
