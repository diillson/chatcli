/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package proc

import (
	"context"
	"regexp"
	"runtime"
	"testing"
	"time"
)

func TestWaitForMatchExitAndTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh-based fixture")
	}
	s := NewSupervisor(nil, nil)
	info, err := s.Start(`sh -c 'echo booting; sleep 0.3; echo "listening on :8080"; sleep 0.5; echo bye'`, "")
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.WaitFor(context.Background(), info.ID, regexp.MustCompile(`(?i)listening on :(\d+)`), 5*time.Second)
	if err != nil || !r.Matched || r.Line != "listening on :8080" || r.Exited || r.TimedOut {
		t.Fatalf("match: %+v %v", r, err)
	}
	// A line already in the output matches at once.
	r, _ = s.WaitFor(context.Background(), info.ID, regexp.MustCompile(`booting`), time.Second)
	if !r.Matched || r.Line != "booting" {
		t.Fatalf("earlier line: %+v", r)
	}
	// No match: the wait ends when the process exits, with the tail.
	r, err = s.WaitFor(context.Background(), info.ID, regexp.MustCompile(`never printed`), 5*time.Second)
	if err != nil || r.Matched || !r.Exited || r.Info.State != StateExited || r.Tail == "" {
		t.Fatalf("exit: %+v %v", r, err)
	}
	// Exit alone (nil pattern) is a condition too.
	r, _ = s.WaitFor(context.Background(), info.ID, nil, time.Second)
	if !r.Exited {
		t.Fatalf("nil pattern waits for exit: %+v", r)
	}

	long, err := s.Start(`sh -c 'echo up; sleep 30'`, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = s.Stop(long.ID) })
	start := time.Now()
	r, err = s.WaitFor(context.Background(), long.ID, regexp.MustCompile(`ready`), 500*time.Millisecond)
	if err != nil || !r.TimedOut || r.Matched || r.Exited || time.Since(start) > 3*time.Second {
		t.Fatalf("timeout: %+v %v (%s)", r, err, time.Since(start))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := s.WaitFor(ctx, long.ID, regexp.MustCompile(`ready`), time.Minute); err == nil {
		t.Fatal("a canceled context ends the wait with its error")
	}
	if _, err := s.WaitFor(context.Background(), "nope", nil, time.Second); err == nil {
		t.Fatal("unknown id must error")
	}

	lines, matched, total, _, err := s.LogsMatching(info.ID, regexp.MustCompile(`(?i)LISTENING|bye`), 1)
	if err != nil || matched != 2 || total != 3 || len(lines) != 1 || lines[0] != "bye" {
		t.Fatalf("logs matching: %v %d/%d %v", lines, matched, total, err)
	}
}
