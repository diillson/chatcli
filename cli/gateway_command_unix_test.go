//go:build !windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestGatewayDetachAttr(t *testing.T) {
	if gatewayDetachAttr() == nil {
		t.Fatal("detach attr should be non-nil")
	}
}

// TestGatewayStopRunning spawns a real sleeping child, points the pidfile at it,
// and verifies /gateway stop terminates it and clears the pidfile.
func TestGatewayStopRunning(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	_ = os.MkdirAll(filepath.Join(tmp, ".chatcli"), 0o750)

	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Skipf("cannot spawn sleep: %v", err)
	}
	defer func() { _ = child.Process.Kill() }()

	pidPath := gatewayStatePath("gateway.pid")
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}

	c := &ChatCLI{logger: zap.NewNop()}
	c.gatewayStatus() // exercises the "running" branch
	c.gatewayStop()   // must terminate the child and remove the pidfile

	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Error("pidfile should be removed after stop")
	}
	// The child should be gone shortly after SIGTERM.
	done := make(chan struct{})
	go func() { _, _ = child.Process.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Error("child was not terminated by gatewayStop")
	}
}

func TestGatewayTerminate(t *testing.T) {
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Skipf("cannot spawn sleep: %v", err)
	}
	if err := gatewayTerminate(child.Process); err != nil {
		t.Fatalf("terminate: %v", err)
	}
	_, _ = child.Process.Wait()
}
