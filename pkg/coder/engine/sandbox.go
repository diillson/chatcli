/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * sandbox.go — OS-level confinement for @coder exec (opt-in).
 *
 * The command validator (IsUnsafeCommand) and the policy manager gate WHAT
 * runs; this layer contains what a permitted command can TOUCH. When enabled,
 * exec is wrapped in the platform sandbox — sandbox-exec on macOS, bubblewrap
 * on Linux — confining file writes to the workspace (plus build caches) and,
 * in strict mode, cutting network access.
 *
 * Deliberately opt-in and default OFF: sandboxing changes exec semantics, and
 * a silently-confined build that can't reach its module cache is worse than
 * no sandbox. CHATCLI_CODER_SANDBOX = off (default) | workspace | strict.
 * Unknown values and a missing sandbox binary degrade to no confinement with
 * one log line — never a hard failure, never a silent block.
 */
package engine

import (
	"os"
	"strings"
)

// SandboxEnv selects the confinement level for exec.
const SandboxEnv = "CHATCLI_CODER_SANDBOX"

// SandboxMode is the resolved confinement level.
type SandboxMode int

const (
	// SandboxOff runs commands unconfined (default).
	SandboxOff SandboxMode = iota
	// SandboxWorkspace confines file writes to the workspace and build caches.
	SandboxWorkspace
	// SandboxStrict is SandboxWorkspace plus no network.
	SandboxStrict
)

// resolveSandboxMode reads CHATCLI_CODER_SANDBOX. Unknown/empty → off.
func resolveSandboxMode() SandboxMode {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(SandboxEnv))) {
	case "workspace", "write", "on":
		return SandboxWorkspace
	case "strict", "no-network", "nonet":
		return SandboxStrict
	default:
		return SandboxOff
	}
}

func (m SandboxMode) String() string {
	switch m {
	case SandboxWorkspace:
		return "workspace"
	case SandboxStrict:
		return "strict"
	default:
		return "off"
	}
}

// sandboxWritablePaths is the generous allowlist of directories a confined
// command may write to beyond the workspace: temp dirs and the language
// toolchains' caches, so `go build`, `npm install`, `pip`, cargo etc. keep
// working. A confinement that broke the module cache would be worse than
// none, so this errs toward permissive.
func sandboxWritablePaths(workspace string) []string {
	paths := []string{workspace}
	if tmp := os.TempDir(); tmp != "" {
		paths = append(paths, tmp)
	}
	paths = append(paths, "/tmp", "/private/tmp", "/private/var/folders", "/dev/null")
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		for _, rel := range []string{
			".chatcli",       // checkpoints, memory, logs
			".cache",         // go-build (linux), pip, many tools
			"Library/Caches", // go-build (macOS), others
			"go/pkg",         // GOMODCACHE
			".npm", ".cargo", ".rustup", ".gradle", ".m2", ".pyenv",
			".cache/go-build",
		} {
			paths = append(paths, home+"/"+rel)
		}
	}
	return dedupPaths(paths)
}

func dedupPaths(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}
