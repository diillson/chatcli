//go:build darwin

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package engine

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildSandboxProfile(t *testing.T) {
	ws := "/work/ws"

	work := buildSandboxProfile(SandboxWorkspace, ws)
	if !strings.Contains(work, "(allow default)") || !strings.Contains(work, "(deny file-write*)") {
		t.Fatalf("workspace profile missing baseline:\n%s", work)
	}
	if !strings.Contains(work, `(subpath "/work/ws")`) {
		t.Fatalf("workspace not in write allowlist:\n%s", work)
	}
	if strings.Contains(work, "(deny network*)") {
		t.Fatal("workspace mode must NOT deny network")
	}

	strict := buildSandboxProfile(SandboxStrict, ws)
	if !strings.Contains(strict, "(deny network*)") {
		t.Fatalf("strict profile must deny network:\n%s", strict)
	}
}

func TestWrapWithSandbox_DarwinShape(t *testing.T) {
	// The wrapper shape is asserted regardless of whether sandbox-exec is
	// on PATH: when absent it degrades, which is a different valid outcome,
	// so only assert structure when it wraps.
	name, args, note := wrapWithSandbox(SandboxStrict, "/ws", "sh", "-c", "echo hi")
	if name == sandboxBinary {
		if args[0] != "-p" || args[len(args)-3] != "sh" || args[len(args)-1] != "echo hi" {
			t.Fatalf("unexpected wrapped args: %v", args)
		}
		if !strings.Contains(note, "strict") {
			t.Fatalf("note should mention strict: %q", note)
		}
	}
}

// TestSandboxExec_ChatcliStateReadOnly runs a real confined command with the
// workspace at $HOME: it may write the workspace, but not ~/.chatcli, where
// the policy, hooks and skills live.
func TestSandboxExec_ChatcliStateReadOnly(t *testing.T) {
	if _, err := exec.LookPath(sandboxBinary); err != nil {
		t.Skip("sandbox-exec not available")
	}
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	state := filepath.Join(home, ".chatcli")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	run := func(target string) error {
		name, args, _ := wrapWithSandbox(SandboxWorkspace, home, "sh", "-c", "echo x > "+target)
		return exec.Command(name, args...).Run() //#nosec G204 -- test-built sandbox invocation over a temp dir
	}
	if err := run(filepath.Join(home, "ok.txt")); err != nil {
		t.Fatalf("the workspace must stay writable: %v", err)
	}
	if err := run(filepath.Join(state, "coder_policy.json")); err == nil {
		t.Fatal("a confined command wrote into ~/.chatcli")
	}
	if _, err := os.Stat(filepath.Join(state, "coder_policy.json")); err == nil {
		t.Fatal("the protected file exists after the confined write")
	}
}
