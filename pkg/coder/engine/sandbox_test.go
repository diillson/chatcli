/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * sandbox_test.go
 *
 * Confinement wrapping: mode resolution, the writable-path allowlist, the
 * off/degrade passthrough, and a real end-to-end check that a sandboxed
 * command still runs when the platform sandbox binary is present.
 */
package engine

import (
	"bytes"
	"context"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

func TestResolveSandboxMode(t *testing.T) {
	cases := map[string]SandboxMode{
		"":          SandboxOff,
		"off":       SandboxOff,
		"garbage":   SandboxOff,
		"workspace": SandboxWorkspace,
		"on":        SandboxWorkspace,
		"strict":    SandboxStrict,
		"nonet":     SandboxStrict,
	}
	for in, want := range cases {
		t.Setenv(SandboxEnv, in)
		if got := resolveSandboxMode(); got != want {
			t.Fatalf("resolveSandboxMode(%q) = %v, want %v", in, got, want)
		}
	}
	if SandboxStrict.String() != "strict" || SandboxOff.String() != "off" {
		t.Fatal("mode strings wrong")
	}
}

func TestSandboxWritablePathsIncludeWorkspaceAndCaches(t *testing.T) {
	paths := sandboxWritablePaths("/work/ws")
	joined := strings.Join(paths, "\n")
	for _, want := range []string{"/work/ws", "go/pkg", ".cache"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("writable paths missing %q:\n%s", want, joined)
		}
	}
	// Deduped: the workspace appears exactly once.
	n := 0
	for _, p := range paths {
		if p == "/work/ws" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("workspace path not deduped (%d occurrences)", n)
	}
}

func TestWrapWithSandbox_OffIsPassthrough(t *testing.T) {
	name, args, note := wrapWithSandbox(SandboxOff, "/ws", "sh", "-c", "echo hi")
	if name != "sh" || len(args) != 2 || args[0] != "-c" || args[1] != "echo hi" || note != "" {
		t.Fatalf("off must be a bare passthrough: %s %v %q", name, args, note)
	}
}

func TestSandboxedCommand_OffMatchesShell(t *testing.T) {
	t.Setenv(SandboxEnv, "off")
	e := NewEngine(&bytes.Buffer{}, &bytes.Buffer{}, t.TempDir())
	name, args := e.sandboxedCommand("echo hi", "")
	shell, flag := resolveShell()
	if name != shell || len(args) != 2 || args[0] != flag || args[1] != "echo hi" {
		t.Fatalf("off sandbox must equal the shell invocation: %s %v", name, args)
	}
}

func TestSandboxedCommand_WrapsWhenAvailable(t *testing.T) {
	if sandboxBinary == "" {
		t.Skip("no sandbox on this platform")
	}
	if _, err := exec.LookPath(sandboxBinary); err != nil {
		t.Skipf("%s not installed", sandboxBinary)
	}
	t.Setenv(SandboxEnv, "workspace")
	var errBuf bytes.Buffer
	e := NewEngine(&bytes.Buffer{}, &errBuf, t.TempDir())
	name, args := e.sandboxedCommand("echo hi", "")
	if name != sandboxBinary {
		t.Fatalf("expected %s wrapper, got %s", sandboxBinary, name)
	}
	if len(args) == 0 {
		t.Fatal("wrapper produced no args")
	}
	if !strings.Contains(errBuf.String(), "sandbox") {
		t.Fatalf("engagement note missing: %q", errBuf.String())
	}
}

func TestSandboxEndToEndEcho(t *testing.T) {
	if sandboxBinary == "" {
		t.Skipf("no sandbox on %s", runtime.GOOS)
	}
	if _, err := exec.LookPath(sandboxBinary); err != nil {
		t.Skipf("%s not installed", sandboxBinary)
	}
	t.Setenv(SandboxEnv, "workspace")

	ws := t.TempDir()
	var out bytes.Buffer
	e := NewEngine(&out, &out, ws)
	// A confined command must still run and produce output.
	if err := e.handleExec(context.Background(), []string{"--cmd", "echo sandbox-ok"}); err != nil {
		t.Fatalf("sandboxed echo failed: %v\noutput:\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "sandbox-ok") {
		t.Fatalf("sandboxed command output missing:\n%s", out.String())
	}
}
