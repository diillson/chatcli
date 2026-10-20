/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestReadOneShotStdin_ReadsToEOF(t *testing.T) {
	got, err := readOneShotStdin(context.Background(), strings.NewReader("  piped input \n"))
	if err != nil || got != "piped input" {
		t.Fatalf("got %q, %v", got, err)
	}
}

// A stdin left open without data must not hang the run past --timeout.
func TestReadOneShotStdin_GivesUpWhenTheTimeoutEnds(t *testing.T) {
	r, w := io.Pipe()
	defer func() { _ = w.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := readOneShotStdin(ctx, r)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the deadline", err)
	}
	if waited := time.Since(start); waited > 2*time.Second {
		t.Fatalf("waited %v for an open stdin", waited)
	}
}
