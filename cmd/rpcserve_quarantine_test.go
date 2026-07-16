/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cmd

import (
	"fmt"
	"os"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// TestQuarantineStdout proves that after quarantine a stray print to
// os.Stdout never reaches the real stdout (the protocol channel) and is
// drained to the logger instead — and that restore puts everything back.
func TestQuarantineStdout(t *testing.T) {
	core, logs := observer.New(zap.DebugLevel)
	logger := zap.New(core)

	orig := os.Stdout
	restore := quarantineStdout(logger)

	if os.Stdout == orig {
		restore()
		t.Fatal("quarantineStdout should re-point os.Stdout away from the protocol channel")
	}

	fmt.Fprintln(os.Stdout, "stray print outside capture")
	restore()

	if os.Stdout != orig {
		t.Fatal("restore should re-point os.Stdout back to the original")
	}

	found := false
	for _, e := range logs.All() {
		for _, f := range e.Context {
			if s, ok := f.Interface.(string); ok && s == "stray print outside capture" {
				found = true
			}
			if f.String == "stray print outside capture" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("the stray print should have been drained into the logger")
	}
}
