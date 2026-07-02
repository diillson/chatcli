//go:build !windows

/*
 * ChatCLI - end-to-end tests for the @proc adapter over the real supervisor.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

// TestProcAdapterLifecycleE2E drives the full loop the model will use —
// start → list → logs → stop → remove — against a REAL process, through the
// same adapter formatting the model reads.
func TestProcAdapterLifecycleE2E(t *testing.T) {
	cli := &ChatCLI{logger: zap.NewNop()}
	t.Cleanup(cli.shutdownProcSupervisor)
	a := &procToolAdapter{cli: cli}

	out, err := a.Start(`sh -c 'echo ready; sleep 30'`, "")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !strings.Contains(out, "started p1 (pid ") {
		t.Fatalf("start output missing id/pid: %q", out)
	}

	list, err := a.List()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(list, "p1 running (pid ") {
		t.Fatalf("list missing running process: %q", list)
	}

	// Poll logs until the readiness line lands (the workflow the tool
	// documents: poll readiness before testing against the process).
	deadline := time.Now().Add(5 * time.Second)
	var logs string
	for time.Now().Before(deadline) {
		logs, err = a.Logs("p1", 10)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(logs, "ready") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(logs, "ready") {
		t.Fatalf("readiness line never appeared: %q", logs)
	}

	stopOut, err := a.Stop("p1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stopOut, "stopped p1") {
		t.Fatalf("stop output wrong: %q", stopOut)
	}

	if _, err := a.Remove("p1"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	empty, err := a.List()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(empty, "No background processes") {
		t.Fatalf("list after remove: %q", empty)
	}
}

// TestProcAdapterEnforcesAgentPolicy pins the security promise: the adapter's
// supervisor uses the agent's CommandValidator, so a command the one-shot
// exec would refuse is refused here too — @proc is not a side door.
func TestProcAdapterEnforcesAgentPolicy(t *testing.T) {
	cli := &ChatCLI{logger: zap.NewNop()}
	t.Cleanup(cli.shutdownProcSupervisor)
	a := &procToolAdapter{cli: cli}

	if _, err := a.Start("sudo rm -rf /", ""); err == nil {
		t.Fatal("dangerous command must be refused by the shared validator")
	}
}
