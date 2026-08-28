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
	"os/exec"
	"strings"
)

// SandboxEnv selects the confinement level for exec.
const SandboxEnv = "CHATCLI_CODER_SANDBOX"

// SandboxImageEnv overrides the container image used by the portable Docker/
// Podman backend. Default: a small image with sh. NOTE: host toolchains
// (go, node…) are NOT available inside the container unless the image ships
// them — the container backend is for confinement-first workflows.
const SandboxImageEnv = "CHATCLI_CODER_SANDBOX_IMAGE"

// defaultSandboxImage is the fallback container image. Alpine is tiny and
// carries a POSIX sh; operators pointing at their own toolchain image set
// CHATCLI_CODER_SANDBOX_IMAGE.
const defaultSandboxImage = "alpine:3"

// containerWorkdir is where the workspace is mounted inside the container.
const containerWorkdir = "/workspace"

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

// resolveSandboxMode reads CHATCLI_CODER_SANDBOX. Unknown/empty → off. The
// container aliases (docker/podman/container) select the portable backend at
// the workspace confinement level; pair with strict via
// CHATCLI_CODER_SANDBOX=strict on a host whose only backend is a container.
func resolveSandboxMode() SandboxMode {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(SandboxEnv))) {
	case "workspace", "write", "on", "docker", "podman", "container":
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

// sandboxImage resolves the container image for the Docker/Podman backend.
func sandboxImage() string {
	if v := strings.TrimSpace(os.Getenv(SandboxImageEnv)); v != "" {
		return v
	}
	return defaultSandboxImage
}

// containerRuntime returns the available container CLI ("docker" or "podman")
// and whether one exists. Docker is preferred; podman is a drop-in fallback.
func containerRuntime() (string, bool) {
	for _, bin := range []string{"docker", "podman"} {
		if _, err := exec.LookPath(bin); err == nil {
			return bin, true
		}
	}
	return "", false
}

// dockerForced reports whether the operator explicitly selected the container
// backend (CHATCLI_CODER_SANDBOX=docker|podman|container).
func dockerForced() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(SandboxEnv))) {
	case "docker", "podman", "container":
		return true
	}
	return false
}

// buildContainerArgs renders the container-runtime arguments that run cmdLine
// under confinement: the workspace bind-mounted read-write, the rest of the
// container's filesystem ephemeral (--rm), and (strict) no network. This is
// the portable backend — identical on Windows, macOS and Linux — used when a
// native OS sandbox is unavailable or explicitly requested.
//
// The command runs as `sh -c cmdLine` INSIDE the image, so host absolute
// paths and host toolchains are not visible; the workspace lives at
// containerWorkdir. Deterministic and unit-testable without a running daemon.
func buildContainerArgs(mode SandboxMode, workspace, cmdLine string) []string {
	args := []string{
		"run", "--rm",
		"--volume", workspace + ":" + containerWorkdir,
		"--workdir", containerWorkdir,
	}
	if mode == SandboxStrict {
		args = append(args, "--network", "none")
	}
	args = append(args, sandboxImage(), "sh", "-c", cmdLine)
	return args
}

// dockerOrDegrade is the shared tail every platform's wrapWithSandbox falls
// back to when its native sandbox binary is absent: use the container runtime
// if one exists (real confinement on ANY OS, Windows included), otherwise run
// unconfined with a note. missingNative is the note to emit when neither a
// native sandbox nor a container runtime is available.
func dockerOrDegrade(mode SandboxMode, workspace, shell, shellFlag, cmdLine, missingNative string) (name string, args []string, note string) {
	if rt, ok := containerRuntime(); ok {
		return rt, buildContainerArgs(mode, workspace, cmdLine), "sandboxed via " + rt + " (" + mode.String() + ")"
	}
	return shell, []string{shellFlag, cmdLine}, missingNative
}
