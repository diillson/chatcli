//go:build !windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"os"
	"testing"
)

// TestEnableStdinCbreakNonTTY pins the safety guard: without a terminal
// (tests, pipes, gateway daemon) the helper is a no-op returning a callable
// restore func — never nil, never touching the process TTY state.
func TestEnableStdinCbreakNonTTY(t *testing.T) {
	// go test runs with stdin on /dev/null or a pipe — not a TTY.
	restore := enableStdinCbreak()
	if restore == nil {
		t.Fatal("restore func must never be nil")
	}
	restore() // must be safely callable
}

// TestSttyHelpersOnNonTTY pins the error path: stty against a non-tty fd
// fails without panicking, which is what lets enableStdinCbreak degrade to
// a no-op on exotic stdin setups.
func TestSttyHelpersOnNonTTY(t *testing.T) {
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open devnull: %v", err)
	}
	defer func() { _ = devnull.Close() }()

	if out, err := sttyOutput(devnull, "-g"); err == nil && out != "" {
		t.Logf("stty -g unexpectedly succeeded on %s (platform quirk): %q", os.DevNull, out)
	}
	_ = sttyRun(devnull, "sane") // must not panic; error is expected and ignored
}

// TestReapplyStdinCbreak pins the re-arm lifecycle: no-op without an active
// reader; with one, the previous restore runs before a fresh state is
// captured.
func TestReapplyStdinCbreak(t *testing.T) {
	a := &AgentMode{}
	a.reapplyStdinCbreak() // no reader — must be a silent no-op
	if a.stdinCbreakRestore != nil {
		t.Fatal("reapply without a reader must not arm cbreak")
	}

	restored := 0
	a.stdinMu.Lock()
	a.stdinLines = make(chan string, 1)
	a.stdinCbreakRestore = func() { restored++ }
	a.stdinMu.Unlock()

	a.reapplyStdinCbreak()
	if restored != 1 {
		t.Fatalf("reapply must run the previous restore exactly once, got %d", restored)
	}
	if a.stdinCbreakRestore == nil {
		t.Fatal("reapply must arm a fresh restore func")
	}
}

// TestTeardownRestoresCbreak pins that tearing the reader down runs the
// cbreak restore and clears the live preview.
func TestTeardownRestoresCbreak(t *testing.T) {
	a := &AgentMode{}
	restored := false
	a.stdinMu.Lock()
	a.stdinDepth = 1
	a.stdinLines = make(chan string, 1)
	a.stdinDone = make(chan struct{})
	a.stdinCbreakRestore = func() { restored = true }
	a.stdinMu.Unlock()
	a.setTypeaheadPreview("meio digitado")

	a.stopStdinReader()
	if !restored {
		t.Fatal("teardown must run the cbreak restore")
	}
	if a.stdinCbreakRestore != nil {
		t.Fatal("teardown must clear the restore func")
	}
	if a.typeaheadPreviewSnapshot() != "" {
		t.Fatal("teardown must clear the live preview")
	}
}

// TestPolicyAdapterRestoreInputHook pins the worker-prompt hook wiring.
func TestPolicyAdapterRestoreInputHook(t *testing.T) {
	a := &workerPolicyAdapter{}
	called := false
	a.setRestoreInput(func() { called = true })
	if a.restoreInput == nil {
		t.Fatal("setter must store the hook")
	}
	a.restoreInput()
	if !called {
		t.Fatal("stored hook must be invocable")
	}
}
