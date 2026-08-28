//go:build !darwin && !linux

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package engine

// sandboxBinary has no meaning on platforms without a supported sandbox.
const sandboxBinary = ""

// wrapWithSandbox is a no-op on unsupported platforms (Windows, *BSD): exec
// runs unconfined. A requested sandbox degrades with a note rather than
// failing — the confinement is a hardening layer, not a gate.
func wrapWithSandbox(mode SandboxMode, _ /*workspace*/, shell, shellFlag, cmdLine string) (name string, args []string, note string) {
	if mode == SandboxOff {
		return shell, []string{shellFlag, cmdLine}, ""
	}
	return shell, []string{shellFlag, cmdLine}, "sandbox not supported on this platform — running unconfined"
}
