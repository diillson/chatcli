/*
 * ChatCLI - Metrics Timer tests
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package metrics

import (
	"context"
	"testing"
	"time"
)

func TestTimer_StartStopElapsed(t *testing.T) {
	tm := NewTimer()
	if tm.IsRunning() {
		t.Fatal("new timer should not be running")
	}

	var ticks int
	tm.Start(context.Background(), func(time.Duration) { ticks++ })
	if !tm.IsRunning() {
		t.Fatal("timer should be running after Start")
	}
	if tm.Elapsed() < 0 {
		t.Error("elapsed should be non-negative while running")
	}
	time.Sleep(20 * time.Millisecond)

	d := tm.Stop()
	if d <= 0 {
		t.Errorf("stopped duration should be positive, got %v", d)
	}
	if tm.IsRunning() {
		t.Error("timer should not be running after Stop")
	}
	// Under `go test` stdout is a pipe, not a TTY, so the animation gate keeps
	// the displayFunc from firing — no spinner noise leaks to captured output.
	if ticks != 0 {
		t.Errorf("displayFunc must not fire when stdout is not a terminal, got %d", ticks)
	}
}

func TestTimer_StopWhenNotRunning(t *testing.T) {
	tm := NewTimer()
	if got := tm.Stop(); got != 0 {
		t.Errorf("Stop on a non-running timer should return 0, got %v", got)
	}
}
