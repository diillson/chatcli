//go:build linux

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package engine

import (
	"os/exec"
)

// sandboxBinary is bubblewrap, the userspace sandbox used on Linux.
const sandboxBinary = "bwrap"

// buildBwrapArgs renders the bubblewrap arguments for the mode: the whole
// filesystem read-only, the workspace and build caches bound read-write,
// private /proc + /dev + /tmp, and (strict) a private network namespace.
func buildBwrapArgs(mode SandboxMode, workspace string) []string {
	args := []string{
		"--ro-bind", "/", "/",
		"--dev", "/dev",
		"--proc", "/proc",
		"--tmpfs", "/tmp",
		"--die-with-parent",
	}
	for _, p := range sandboxWritablePaths(workspace) {
		// Bind each writable path over the read-only root. --bind-try skips
		// paths that do not exist rather than aborting the whole sandbox.
		args = append(args, "--bind-try", p, p)
	}
	if mode == SandboxStrict {
		args = append(args, "--unshare-net")
	}
	return args
}

// wrapWithSandbox prepends the bwrap invocation to (shell, shellFlag,
// cmdLine). Returns the unchanged trio when confinement is off or bwrap is
// unavailable, plus a note for the caller to log.
func wrapWithSandbox(mode SandboxMode, workspace, shell, shellFlag, cmdLine string) (name string, args []string, note string) {
	if mode == SandboxOff {
		return shell, []string{shellFlag, cmdLine}, ""
	}
	if _, err := exec.LookPath(sandboxBinary); err != nil {
		return shell, []string{shellFlag, cmdLine}, "sandbox requested but bwrap not found — running unconfined"
	}
	args = append(buildBwrapArgs(mode, workspace), shell, shellFlag, cmdLine)
	return sandboxBinary, args, "sandboxed (" + mode.String() + ")"
}
