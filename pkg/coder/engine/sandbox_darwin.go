//go:build darwin

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package engine

import (
	"fmt"
	"os/exec"
	"strings"
)

// sandboxBinary is macOS's built-in sandbox wrapper.
const sandboxBinary = "sandbox-exec"

// buildSandboxProfile renders a sandbox-exec SBPL profile for the mode. It
// starts from "allow default" and subtracts capabilities — the least
// surprising baseline for a build tool: reads stay open, writes are confined,
// and (strict) the network is cut.
func buildSandboxProfile(mode SandboxMode, workspace string) string {
	var b strings.Builder
	b.WriteString("(version 1)\n(allow default)\n")
	if mode == SandboxStrict {
		b.WriteString("(deny network*)\n")
	}
	b.WriteString("(deny file-write*)\n")
	b.WriteString("(allow file-write*\n")
	for _, p := range sandboxWritablePaths(workspace) {
		fmt.Fprintf(&b, "  (subpath %q)\n", p)
	}
	// Terminals and standard streams must stay writable or the command's own
	// output is lost.
	b.WriteString("  (literal \"/dev/stdout\")\n  (literal \"/dev/stderr\")\n  (regex #\"^/dev/tty\")\n)\n")
	return b.String()
}

// wrapWithSandbox confines (shell, shellFlag, cmdLine). Selection order:
// an explicit container request wins; otherwise the native sandbox-exec is
// used; if it is somehow unavailable, the portable container backend takes
// over (real confinement on any host), degrading to unconfined only when no
// backend exists at all.
func wrapWithSandbox(mode SandboxMode, workspace, shell, shellFlag, cmdLine string) (name string, args []string, note string) {
	if mode == SandboxOff {
		return shell, []string{shellFlag, cmdLine}, ""
	}
	if dockerForced() {
		return dockerOrDegrade(mode, workspace, shell, shellFlag, cmdLine,
			"container sandbox requested but no docker/podman found — running unconfined")
	}
	if _, err := exec.LookPath(sandboxBinary); err != nil {
		return dockerOrDegrade(mode, workspace, shell, shellFlag, cmdLine,
			"no sandbox-exec and no docker/podman found — running unconfined")
	}
	profile := buildSandboxProfile(mode, workspace)
	return sandboxBinary, []string{"-p", profile, shell, shellFlag, cmdLine}, "sandboxed (" + mode.String() + ")"
}
