/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package metrics

import (
	"context"
	"testing"
)

// TestTimerNestedPause pins the refcounted pause contract: with two
// concurrent pausers (security prompt + mid-run side command), the display
// only resumes after BOTH resume.
func TestTimerNestedPause(t *testing.T) {
	tm := NewTimer()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tm.Start(ctx, nil)
	// Simulate a ticker being armed (Start skips it for nil displayFunc /
	// non-TTY); Resume requires cancel != nil to flip running back.
	tm.mu.Lock()
	tm.cancel = cancel
	tm.mu.Unlock()

	tm.Pause() // security prompt
	tm.Pause() // side command while the prompt is up
	if tm.IsRunning() {
		t.Fatal("paused timer must not be running")
	}
	tm.Resume() // side command done — prompt still owns the terminal
	if tm.IsRunning() {
		t.Fatal("display must stay paused until every pauser resumes")
	}
	tm.Resume() // prompt done
	if !tm.IsRunning() {
		t.Fatal("display must resume once the pause depth drains to zero")
	}
	if d := tm.Stop(); d <= 0 {
		t.Errorf("elapsed must accumulate across pauses, got %v", d)
	}
}
