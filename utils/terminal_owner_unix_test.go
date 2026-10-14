//go:build !windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package utils

import (
	"bufio"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

const ownsTerminalProbe = "CHATCLI_OWNS_TERMINAL_PROBE"

// TestOwnsTerminalProbe is the child half: it prints the verdict and exits.
func TestOwnsTerminalProbe(t *testing.T) {
	if os.Getenv(ownsTerminalProbe) != "1" {
		t.Skip("probe helper; run by TestOwnsTerminal")
	}
	if OwnsTerminal() {
		os.Stdout.WriteString("OWNS=true\n")
	} else {
		os.Stdout.WriteString("OWNS=false\n")
	}
}

func probeOwnership(t *testing.T, cmd *exec.Cmd) string {
	t.Helper()
	cmd.Env = append(os.Environ(), ownsTerminalProbe+"=1")
	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Skipf("no pty available: %v", err)
	}
	defer ptmx.Close()
	done := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(ptmx)
		for sc.Scan() {
			if l := sc.Text(); strings.HasPrefix(l, "OWNS=") {
				done <- strings.TrimSpace(l)
				return
			}
		}
		done <- ""
	}()
	select {
	case v := <-done:
		_ = cmd.Wait()
		return v
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("probe timed out")
		return ""
	}
}

// The process running in the foreground of its terminal owns it; a child
// that shares the terminal but reads stdin from elsewhere (the /web UI
// process) does not, so it can never change the terminal's mode.
func TestOwnsTerminal(t *testing.T) {
	owner := exec.Command(os.Args[0], "-test.run=^TestOwnsTerminalProbe$")
	if got := probeOwnership(t, owner); got != "OWNS=true" {
		t.Fatalf("foreground process on its terminal: %q, want OWNS=true", got)
	}

	child := exec.Command(os.Args[0], "-test.run=^TestOwnsTerminalProbe$")
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()
	child.Env = append(os.Environ(), ownsTerminalProbe+"=1")
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("no pty available: %v", err)
	}
	defer ptmx.Close()
	defer tty.Close()
	child.Stdin, child.Stdout, child.Stderr = devnull, tty, tty
	out := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(ptmx)
		for sc.Scan() {
			if l := sc.Text(); strings.HasPrefix(l, "OWNS=") {
				out <- strings.TrimSpace(l)
				return
			}
		}
	}()
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-out:
		if got != "OWNS=false" {
			t.Fatalf("child with stdin redirected: %q, want OWNS=false", got)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("child probe timed out")
	}
	_ = child.Wait()
}

func TestOwnsTerminalWithoutATerminal(t *testing.T) {
	if os.Getenv(ownsTerminalProbe) == "1" {
		t.Skip("inside the probe")
	}
	// go test runs without a terminal on stdin in CI; locally stdin may be
	// one, so only assert the CI-shaped case.
	if f, err := os.Stdin.Stat(); err == nil && f.Mode()&os.ModeCharDevice == 0 && OwnsTerminal() {
		t.Fatal("a process whose stdin is not a terminal cannot own one")
	}
}
