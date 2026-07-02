//go:build !windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Supervisor tests run REAL processes (sh/echo/sleep) — the supervisor's
 * whole job is OS-process lifecycle, and faking that would test nothing.
 */
package proc

import (
	"errors"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

func waitState(t *testing.T, s *Supervisor, id string, want State, timeout time.Duration) Info {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		info, err := s.Status(id)
		if err != nil {
			t.Fatal(err)
		}
		if info.State == want {
			return info
		}
		time.Sleep(20 * time.Millisecond)
	}
	info, _ := s.Status(id)
	t.Fatalf("process %s never reached %s (state=%s)", id, want, info.State)
	return Info{}
}

func TestSupervisorRunCaptureExit(t *testing.T) {
	s := NewSupervisor(nil, zap.NewNop())
	defer s.CloseAll()

	info, err := s.Start(`sh -c 'echo hello-out; echo hello-err 1>&2; exit 3'`, "")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if info.State != StateRunning || info.PID == 0 {
		t.Fatalf("start info wrong: %+v", info)
	}

	final := waitState(t, s, info.ID, StateExited, 5*time.Second)
	if final.ExitCode != 3 {
		t.Fatalf("ExitCode = %d, want 3", final.ExitCode)
	}
	logs, _, err := s.Logs(info.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs, "hello-out") || !strings.Contains(logs, "hello-err") {
		t.Fatalf("combined output missing streams: %q", logs)
	}
}

func TestSupervisorStopTerminatesProcessTree(t *testing.T) {
	s := NewSupervisor(nil, zap.NewNop())
	defer s.CloseAll()

	// A parent that spawns a child sleeper: Stop must take BOTH down (the
	// process-group signal), not just the shell.
	info, err := s.Start(`sh -c 'sleep 30 & wait'`, "")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond) // let the child spawn

	stopped, err := s.Stop(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.State != StateExited {
		t.Fatalf("state after Stop = %s, want exited", stopped.State)
	}
	// Stop is idempotent.
	if _, err := s.Stop(info.ID); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}

func TestSupervisorLogsTail(t *testing.T) {
	s := NewSupervisor(nil, zap.NewNop())
	defer s.CloseAll()

	info, err := s.Start(`sh -c 'for i in $(seq 1 50); do echo line-$i; done'`, "")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, s, info.ID, StateExited, 5*time.Second)

	logs, _, err := s.Logs(info.ID, 5)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(logs, "\n")
	if len(lines) != 5 || lines[4] != "line-50" || lines[0] != "line-46" {
		t.Fatalf("tail wrong: %q", logs)
	}
}

func TestSupervisorValidatorGate(t *testing.T) {
	blocked := errors.New("blocked by policy")
	s := NewSupervisor(func(cmd string) error {
		if strings.Contains(cmd, "rm -rf") {
			return blocked
		}
		return nil
	}, zap.NewNop())
	defer s.CloseAll()

	if _, err := s.Start("rm -rf /tmp/x", ""); !errors.Is(err, blocked) {
		t.Fatalf("validator not enforced: %v", err)
	}
	if _, err := s.Start("echo ok", ""); err != nil {
		t.Fatalf("allowed command rejected: %v", err)
	}
}

func TestSupervisorRunningLimit(t *testing.T) {
	s := NewSupervisor(nil, zap.NewNop())
	defer s.CloseAll()

	for i := 0; i < maxRunning; i++ {
		if _, err := s.Start("sleep 30", ""); err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
	}
	if _, err := s.Start("sleep 30", ""); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("running limit not enforced: %v", err)
	}
}

func TestSupervisorRingBufferBounds(t *testing.T) {
	s := NewSupervisor(nil, zap.NewNop())
	defer s.CloseAll()

	// ~1MiB of output must be trimmed to the ring cap without breaking lines.
	info, err := s.Start(`sh -c 'i=0; while [ $i -lt 16384 ]; do echo "0123456789012345678901234567890123456789012345678901234567890 $i"; i=$((i+1)); done'`, "")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, s, info.ID, StateExited, 15*time.Second)

	logs, _, err := s.Logs(info.ID, MaxTailLines)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) > outputRingCap {
		t.Fatalf("ring buffer exceeded cap: %d bytes", len(logs))
	}
	if !strings.Contains(logs, " 16383") {
		t.Fatal("newest output must survive trimming")
	}
	if strings.HasPrefix(logs, "0123456789") == false && !strings.HasPrefix(logs, "0") {
		t.Fatalf("buffer must start at a line boundary, got %.40q", logs)
	}
}

func TestSupervisorCloseAllStopsEverything(t *testing.T) {
	s := NewSupervisor(nil, zap.NewNop())
	a, err := s.Start("sleep 30", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Start("sleep 30", "")
	if err != nil {
		t.Fatal(err)
	}

	s.CloseAll()
	for _, id := range []string{a.ID, b.ID} {
		info, serr := s.Status(id)
		if serr != nil {
			t.Fatal(serr)
		}
		if info.State != StateExited {
			t.Fatalf("%s still %s after CloseAll", id, info.State)
		}
	}
	if _, err := s.Start("echo late", ""); err == nil {
		t.Fatal("closed supervisor must refuse new starts")
	}
}

func TestSupervisorRemoveAndUnknownID(t *testing.T) {
	s := NewSupervisor(nil, zap.NewNop())
	defer s.CloseAll()

	info, err := s.Start("echo bye", "")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, s, info.ID, StateExited, 5*time.Second)

	running, err := s.Start("sleep 30", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(running.ID); err == nil {
		t.Fatal("removing a running process must fail")
	}
	if err := s.Remove(info.ID); err != nil {
		t.Fatalf("Remove exited: %v", err)
	}
	if _, err := s.Status(info.ID); err == nil || !strings.Contains(err.Error(), running.ID) {
		t.Fatalf("unknown-id error must list tracked ids, got %v", err)
	}
}
