//go:build !darwin && !linux

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package engine

// sandboxBinary has no native meaning on platforms without a built-in
// sandbox (Windows, *BSD): confinement there is delivered by the portable
// container backend.
const sandboxBinary = ""

// wrapWithSandbox confines (shell, shellFlag, cmdLine) on platforms with no
// native sandbox — Windows above all — through the portable Docker/Podman
// backend, so the feature genuinely confines on ANY OS instead of silently
// running unconfined. Degrades only when no container runtime is installed.
func wrapWithSandbox(mode SandboxMode, workspace, shell, shellFlag, cmdLine string) (name string, args []string, note string) {
	if mode == SandboxOff {
		return shell, []string{shellFlag, cmdLine}, ""
	}
	return dockerOrDegrade(mode, workspace, shell, shellFlag, cmdLine,
		"sandbox requested but no docker/podman found on this platform — running unconfined")
}
