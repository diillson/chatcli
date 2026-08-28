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

func TestBuildContainerArgs(t *testing.T) {
	t.Setenv(SandboxImageEnv, "")
	ws := "/work/ws"

	work := strings.Join(buildContainerArgs(SandboxWorkspace, ws, "echo hi"), " ")
	for _, want := range []string{"run --rm", "--volume /work/ws:/workspace", "--workdir /workspace", "alpine:3", "sh -c echo hi"} {
		if !strings.Contains(work, want) {
			t.Fatalf("container args missing %q:\n%s", want, work)
		}
	}
	if strings.Contains(work, "--network none") {
		t.Fatal("workspace mode must NOT cut the network")
	}

	strict := strings.Join(buildContainerArgs(SandboxStrict, ws, "echo hi"), " ")
	if !strings.Contains(strict, "--network none") {
		t.Fatalf("strict container must cut the network:\n%s", strict)
	}
}

func TestSandboxImageOverride(t *testing.T) {
	t.Setenv(SandboxImageEnv, "myrepo/toolchain:latest")
	if got := sandboxImage(); got != "myrepo/toolchain:latest" {
		t.Fatalf("image override ignored: %s", got)
	}
	t.Setenv(SandboxImageEnv, "")
	if got := sandboxImage(); got != defaultSandboxImage {
		t.Fatalf("default image wrong: %s", got)
	}
}

func TestDockerForcedAndModeAliases(t *testing.T) {
	for _, alias := range []string{"docker", "podman", "container"} {
		t.Setenv(SandboxEnv, alias)
		if !dockerForced() {
			t.Fatalf("%q must force the container backend", alias)
		}
		if resolveSandboxMode() != SandboxWorkspace {
			t.Fatalf("%q must resolve to a confinement mode", alias)
		}
	}
	t.Setenv(SandboxEnv, "workspace")
	if dockerForced() {
		t.Fatal("workspace mode must not force docker")
	}
}

func TestDockerOrDegrade_NoRuntime(t *testing.T) {
	// This unit runs regardless of whether docker is installed: when it is,
	// the container path is exercised; when not, the degrade path is. Both
	// are valid — assert the invariant that SOMETHING runnable comes back.
	name, args, note := dockerOrDegrade(SandboxStrict, "/ws", "sh", "-c", "echo hi", "none-found")
	if name == "" || len(args) == 0 {
		t.Fatalf("must always return a runnable command: %s %v", name, args)
	}
	if _, ok := containerRuntime(); !ok {
		if name != "sh" || note != "none-found" {
			t.Fatalf("without a runtime it must degrade to the shell: %s %q", name, note)
		}
	}
}

func TestSandboxContainerEndToEnd(t *testing.T) {
	rt, ok := containerRuntime()
	if !ok {
		t.Skip("no docker/podman available")
	}
	// A trivial daemon liveness probe; skip if the runtime is installed but
	// the daemon is not reachable (common in CI).
	if err := exec.Command(rt, "info").Run(); err != nil {
		t.Skipf("%s daemon not reachable", rt)
	}
	t.Setenv(SandboxEnv, "docker")
	t.Setenv(SandboxImageEnv, "alpine:3")

	ws := t.TempDir()
	var out bytes.Buffer
	e := NewEngine(&out, &out, ws)
	if err := e.handleExec(context.Background(), []string{"--cmd", "echo container-ok", "--timeout", "120"}); err != nil {
		t.Fatalf("containerized echo failed: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "container-ok") {
		t.Fatalf("container command output missing:\n%s", out.String())
	}
}
