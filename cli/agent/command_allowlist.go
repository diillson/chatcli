/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package agent

import (
	"os"
	"strings"
	"sync"
)

// SecurityMode determines how command validation works.
type SecurityMode string

const (
	// SecurityModeStrict only allows commands in the allowlist (default).
	SecurityModeStrict SecurityMode = "strict"
	// SecurityModePermissive uses allowlist + falls back to denylist for unknown commands.
	SecurityModePermissive SecurityMode = "permissive"
)

// CommandAllowlist validates commands against a categorized allowlist.
// In strict mode, only allowed commands can execute.
// In permissive mode, unknown commands fall back to the legacy denylist validator.
type CommandAllowlist struct {
	mu             sync.RWMutex
	allowedCommands map[string]string // command -> category
	mode           SecurityMode
}

// DefaultAllowedCommands returns the categorized command allowlist.
func DefaultAllowedCommands() map[string]string {
	commands := map[string]string{
		// File operations
		"ls": "file", "cat": "file", "head": "file", "tail": "file",
		"wc": "file", "find": "file", "file": "file", "stat": "file",
		"du": "file", "df": "file", "tree": "file", "mkdir": "file",
		"cp": "file", "mv": "file", "touch": "file", "rm": "file",
		"ln": "file", "chmod": "file", "chown": "file", "basename": "file",
		"dirname": "file", "realpath": "file", "readlink": "file",
		"md5sum": "file", "sha256sum": "file", "sha1sum": "file",

		// Text processing
		"grep": "text", "rg": "text", "sed": "text", "awk": "text",
		"sort": "text", "uniq": "text", "cut": "text", "tr": "text",
		"diff": "text", "jq": "text", "yq": "text", "xargs": "text",
		"tee": "text", "paste": "text", "column": "text", "fmt": "text",
		"fold": "text", "expand": "text", "unexpand": "text",
		"comm": "text", "join": "text", "nl": "text", "rev": "text",
		"strings": "text", "od": "text", "xxd": "text", "hexdump": "text",

		// Development tools
		"go": "dev", "git": "dev", "make": "dev", "npm": "dev",
		"yarn": "dev", "pnpm": "dev", "pip": "dev", "pip3": "dev",
		"python": "dev", "python3": "dev", "node": "dev",
		"cargo": "dev", "rustc": "dev", "rustup": "dev",
		"javac": "dev", "java": "dev", "mvn": "dev", "gradle": "dev",
		"gcc": "dev", "g++": "dev", "clang": "dev", "cmake": "dev",
		"swift": "dev", "swiftc": "dev", "dotnet": "dev",
		"ruby": "dev", "gem": "dev", "bundle": "dev",
		"php": "dev", "composer": "dev",
		"tsc": "dev", "deno": "dev", "bun": "dev",
		"gofmt": "dev", "golint": "dev", "gopls": "dev",
		"eslint": "dev", "prettier": "dev", "black": "dev",
		"pytest": "dev", "jest": "dev", "mocha": "dev",

		// Container / Infrastructure
		"docker": "container", "podman": "container",
		"kubectl": "container", "helm": "container",
		"terraform": "container", "terragrunt": "container",
		"kind": "container", "minikube": "container",
		"docker-compose": "container", "skaffold": "container",
		"kustomize": "container", "oc": "container",
		"eksctl": "container", "gcloud": "container",
		"aws": "container", "az": "container",

		// Network (read-only oriented)
		"curl": "network", "wget": "network",
		"dig": "network", "nslookup": "network",
		"ping": "network", "traceroute": "network",
		"host": "network", "whois": "network",
		"ssh": "network", "scp": "network", "rsync": "network",
		"nc": "network", "netstat": "network", "ss": "network",

		// System info (read-only)
		"uname": "sysinfo", "whoami": "sysinfo", "date": "sysinfo",
		"env": "sysinfo", "printenv": "sysinfo", "hostname": "sysinfo",
		"uptime": "sysinfo", "free": "sysinfo", "top": "sysinfo",
		"ps": "sysinfo", "which": "sysinfo", "whereis": "sysinfo",
		"id": "sysinfo", "groups": "sysinfo", "lsof": "sysinfo",
		"ulimit": "sysinfo", "locale": "sysinfo", "getconf": "sysinfo",
		"arch": "sysinfo", "nproc": "sysinfo", "lscpu": "sysinfo",
		"lsblk": "sysinfo", "mount": "sysinfo", "lsusb": "sysinfo",

		// Editors / Viewers
		"code": "editor", "vim": "editor", "nvim": "editor",
		"nano": "editor", "less": "editor", "more": "editor",
		"bat": "editor", "vi": "editor", "emacs": "editor",

		// Shell utilities
		"echo": "shell", "printf": "shell", "test": "shell",
		"true": "shell", "false": "shell", "sleep": "shell",
		"seq": "shell", "yes": "shell", "timeout": "shell",
		"watch": "shell", "time": "shell", "strace": "shell",
		"export": "shell", "set": "shell", "unset": "shell",
		"alias": "shell", "type": "shell", "command": "shell",
		"source": "shell", "eval": "shell", "exec": "shell",
		"sh": "shell", "bash": "shell", "zsh": "shell",
	}
	return commands
}

// NewCommandAllowlist creates a new allowlist validator configured from environment.
func NewCommandAllowlist() *CommandAllowlist {
	mode := SecurityModeStrict
	if m := os.Getenv("CHATCLI_AGENT_SECURITY_MODE"); strings.EqualFold(m, "permissive") {
		mode = SecurityModePermissive
	}

	al := &CommandAllowlist{
		allowedCommands: DefaultAllowedCommands(),
		mode:           mode,
	}

	// Add custom commands from CHATCLI_AGENT_ALLOWLIST env var (comma-separated)
	if extra := os.Getenv("CHATCLI_AGENT_ALLOWLIST"); extra != "" {
		for _, cmd := range strings.Split(extra, ",") {
			cmd = strings.TrimSpace(cmd)
			if cmd != "" {
				al.allowedCommands[cmd] = "custom"
			}
		}
	}

	return al
}

// IsAllowed checks if a command is in the allowlist.
// Returns (allowed, category, reason).
func (al *CommandAllowlist) IsAllowed(fullCommand string) (bool, string, string) {
	al.mu.RLock()
	defer al.mu.RUnlock()

	baseCmd := extractBaseCommand(fullCommand)
	if baseCmd == "" {
		return false, "", "empty command"
	}

	if category, ok := al.allowedCommands[baseCmd]; ok {
		return true, category, ""
	}

	return false, "", "command '" + baseCmd + "' is not in the security allowlist"
}

// GetMode returns the current security mode.
func (al *CommandAllowlist) GetMode() SecurityMode {
	return al.mode
}

// extractBaseCommand extracts the primary command from a full shell command string.
// Handles pipes, subshells, command substitution, env var prefixes, sudo, etc.
func extractBaseCommand(fullCommand string) string {
	cmd := strings.TrimSpace(fullCommand)
	if cmd == "" {
		return ""
	}

	// Strip leading env var assignments (FOO=bar BAR=baz command ...)
	for {
		if idx := strings.IndexByte(cmd, '='); idx > 0 {
			prefix := cmd[:idx]
			// Check if prefix looks like a var name (no spaces, alphanumeric+underscore)
			isVar := true
			for _, c := range prefix {
				if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_') {
					isVar = false
					break
				}
			}
			if isVar {
				// Skip past the value
				rest := cmd[idx+1:]
				// Skip quoted value
				if len(rest) > 0 && (rest[0] == '"' || rest[0] == '\'') {
					q := rest[0]
					end := strings.IndexByte(rest[1:], q)
					if end >= 0 {
						rest = strings.TrimSpace(rest[end+2:])
					}
				} else {
					// Skip unquoted value (until space)
					sp := strings.IndexByte(rest, ' ')
					if sp >= 0 {
						rest = strings.TrimSpace(rest[sp+1:])
					} else {
						rest = ""
					}
				}
				if rest == "" {
					return ""
				}
				cmd = rest
				continue
			}
		}
		break
	}

	// Strip sudo/doas prefix
	for _, prefix := range []string{"sudo ", "doas "} {
		if strings.HasPrefix(cmd, prefix) {
			cmd = strings.TrimSpace(cmd[len(prefix):])
			// Skip sudo flags like -u user
			for strings.HasPrefix(cmd, "-") {
				sp := strings.IndexByte(cmd, ' ')
				if sp < 0 {
					return ""
				}
				cmd = strings.TrimSpace(cmd[sp+1:])
				// If the flag takes an argument (e.g., -u root), skip that too
				if !strings.HasPrefix(cmd, "-") {
					break
				}
			}
		}
	}

	// Take only the first command in a pipeline or chain
	for _, sep := range []string{"|", "&&", "||", ";", "&"} {
		if idx := strings.Index(cmd, sep); idx >= 0 {
			cmd = strings.TrimSpace(cmd[:idx])
		}
	}

	// Handle command substitution — extract the outermost command
	cmd = strings.TrimLeft(cmd, "(")

	// Take the first word
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return ""
	}

	baseCmd := fields[0]
	// Strip path prefix (e.g., /usr/bin/git -> git)
	if idx := strings.LastIndex(baseCmd, "/"); idx >= 0 {
		baseCmd = baseCmd[idx+1:]
	}

	return baseCmd
}
