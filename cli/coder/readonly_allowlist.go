/*
 * ChatCLI - Read-Only Command Allowlist
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Centralized allowlist of commands that are safe to execute without user
 * approval because they only read data and have no side effects.
 *
 * Inspired by openclaude's readOnlyValidation.ts which maintains a
 * COMMAND_ALLOWLIST with per-flag safe values.
 *
 * Commands in this list are auto-allowed in the policy check flow,
 * bypassing the interactive prompt. This significantly reduces prompt
 * fatigue for common read-only operations.
 */
package coder

import "strings"

// ReadOnlyCommand defines a command that is safe for auto-approval.
type ReadOnlyCommand struct {
	// Name is the base command name (e.g., "git", "ls", "cat")
	Name string

	// SafeSubcommands are subcommands that are read-only.
	// If empty, the command itself is read-only (e.g., "ls").
	// If non-empty, only these subcommands are auto-allowed.
	SafeSubcommands []string

	// UnsafeFlags are flags that make even a safe command unsafe.
	// If the command contains any of these, it's NOT auto-allowed.
	UnsafeFlags []string
}

// readOnlyAllowlist is the centralized list of read-only commands.
var readOnlyAllowlist = []ReadOnlyCommand{
	// Filesystem read operations
	{Name: "ls", UnsafeFlags: []string{}},
	{Name: "ll", UnsafeFlags: []string{}},
	{Name: "cat", UnsafeFlags: []string{}},
	{Name: "head", UnsafeFlags: []string{}},
	{Name: "tail", UnsafeFlags: []string{"-f"}}, // -f follows = long-running, not pure read
	{Name: "less", UnsafeFlags: []string{}},
	{Name: "more", UnsafeFlags: []string{}},
	{Name: "wc", UnsafeFlags: []string{}},
	{Name: "file", UnsafeFlags: []string{}},
	{Name: "stat", UnsafeFlags: []string{}},
	{Name: "du", UnsafeFlags: []string{}},
	{Name: "df", UnsafeFlags: []string{}},
	{Name: "find", UnsafeFlags: []string{"-delete", "-exec", "-execdir"}},
	{Name: "tree", UnsafeFlags: []string{}},
	{Name: "realpath", UnsafeFlags: []string{}},
	{Name: "readlink", UnsafeFlags: []string{}},
	{Name: "basename", UnsafeFlags: []string{}},
	{Name: "dirname", UnsafeFlags: []string{}},
	{Name: "md5sum", UnsafeFlags: []string{}},
	{Name: "sha256sum", UnsafeFlags: []string{}},

	// Text processing (read-only)
	{Name: "grep", UnsafeFlags: []string{}},
	{Name: "egrep", UnsafeFlags: []string{}},
	{Name: "fgrep", UnsafeFlags: []string{}},
	{Name: "rg", UnsafeFlags: []string{}},    // ripgrep
	{Name: "ag", UnsafeFlags: []string{}},    // silversearcher
	{Name: "awk", UnsafeFlags: []string{}},   // read-only by default
	{Name: "sed", UnsafeFlags: []string{"-i", "--in-place"}}, // -i modifies in-place
	{Name: "sort", UnsafeFlags: []string{"-o"}}, // -o writes to file
	{Name: "uniq", UnsafeFlags: []string{}},
	{Name: "cut", UnsafeFlags: []string{}},
	{Name: "tr", UnsafeFlags: []string{}},
	{Name: "diff", UnsafeFlags: []string{}},
	{Name: "comm", UnsafeFlags: []string{}},
	{Name: "jq", UnsafeFlags: []string{}},
	{Name: "yq", UnsafeFlags: []string{}},
	{Name: "xmllint", UnsafeFlags: []string{}},

	// Git read operations
	{Name: "git", SafeSubcommands: []string{
		"status", "diff", "log", "show", "branch", "tag",
		"remote", "stash", "list", "rev-parse", "describe",
		"ls-files", "ls-tree", "cat-file", "blame", "shortlog",
		"reflog", "config", // config is read-only without --global/--system set
	}, UnsafeFlags: []string{"--global", "--system"}},

	// Development tools (read-only invocations)
	{Name: "go", SafeSubcommands: []string{
		"version", "env", "list", "doc", "vet",
	}},
	{Name: "node", SafeSubcommands: []string{"-v", "--version", "-e"}},
	{Name: "python", SafeSubcommands: []string{"--version", "-c"}},
	{Name: "python3", SafeSubcommands: []string{"--version", "-c"}},
	{Name: "rustc", SafeSubcommands: []string{"--version"}},
	{Name: "java", SafeSubcommands: []string{"-version", "--version"}},

	// System info
	{Name: "uname", UnsafeFlags: []string{}},
	{Name: "hostname", UnsafeFlags: []string{}},
	{Name: "whoami", UnsafeFlags: []string{}},
	{Name: "id", UnsafeFlags: []string{}},
	{Name: "date", UnsafeFlags: []string{}},
	{Name: "uptime", UnsafeFlags: []string{}},
	{Name: "env", UnsafeFlags: []string{}},
	{Name: "printenv", UnsafeFlags: []string{}},
	{Name: "which", UnsafeFlags: []string{}},
	{Name: "whereis", UnsafeFlags: []string{}},
	{Name: "type", UnsafeFlags: []string{}},

	// Container/K8s read operations
	{Name: "docker", SafeSubcommands: []string{
		"ps", "images", "inspect", "logs", "info", "version", "stats",
		"network", "volume", "ls", "top",
	}},
	{Name: "kubectl", SafeSubcommands: []string{
		"get", "describe", "logs", "top", "version", "config",
		"cluster-info", "api-resources", "api-versions",
	}},

	// Package managers (read-only)
	{Name: "npm", SafeSubcommands: []string{"ls", "list", "outdated", "info", "view", "search", "config"}},
	{Name: "yarn", SafeSubcommands: []string{"list", "info", "outdated", "config"}},
	{Name: "pip", SafeSubcommands: []string{"list", "show", "freeze", "check"}},
	{Name: "pip3", SafeSubcommands: []string{"list", "show", "freeze", "check"}},
	{Name: "cargo", SafeSubcommands: []string{"tree", "metadata", "version"}},

	// Echo/printf (output only, no side effects)
	{Name: "echo", UnsafeFlags: []string{}},
	{Name: "printf", UnsafeFlags: []string{}},
	{Name: "pwd", UnsafeFlags: []string{}},
}

// IsReadOnlyCommand checks if a shell command is safe to auto-approve.
// Returns true if the command is in the read-only allowlist and doesn't
// contain any unsafe flags.
func IsReadOnlyCommand(cmdLine string) bool {
	cmdLine = strings.TrimSpace(cmdLine)
	if cmdLine == "" {
		return false
	}

	// Reject commands with pipe chains that could have side effects
	// e.g., "grep foo | rm" — the second command is dangerous
	if containsDangerousPipeTarget(cmdLine) {
		return false
	}

	// Reject commands with output redirection (could write files)
	if containsOutputRedirect(cmdLine) {
		return false
	}

	// Parse the base command and arguments
	parts := strings.Fields(cmdLine)
	baseCmd := parts[0]

	// Strip path prefix (e.g., "/usr/bin/git" → "git")
	if idx := strings.LastIndex(baseCmd, "/"); idx >= 0 {
		baseCmd = baseCmd[idx+1:]
	}

	for _, entry := range readOnlyAllowlist {
		if !strings.EqualFold(entry.Name, baseCmd) {
			continue
		}

		// Check subcommands if required
		if len(entry.SafeSubcommands) > 0 {
			if len(parts) < 2 {
				return false // need at least a subcommand
			}
			subcmd := parts[1]
			if !containsIgnoreCase(entry.SafeSubcommands, subcmd) {
				return false // subcommand not in safe list
			}
		}

		// Check for unsafe flags
		for _, flag := range entry.UnsafeFlags {
			for _, part := range parts[1:] {
				if strings.EqualFold(part, flag) {
					return false // contains unsafe flag
				}
			}
		}

		return true
	}

	return false
}

// containsDangerousPipeTarget checks if a piped command chain contains
// a potentially dangerous command after a pipe.
func containsDangerousPipeTarget(cmdLine string) bool {
	// Simple heuristic: check if anything after | is dangerous
	pipeIdx := strings.Index(cmdLine, "|")
	if pipeIdx < 0 {
		return false
	}

	after := strings.TrimSpace(cmdLine[pipeIdx+1:])
	if after == "" {
		return false
	}

	// Get the command after the pipe
	parts := strings.Fields(after)
	if len(parts) == 0 {
		return false
	}

	dangerousTargets := map[string]bool{
		"rm": true, "dd": true, "mkfs": true, "tee": true,
		"sh": true, "bash": true, "zsh": true, "eval": true,
		"xargs": true, // xargs can execute anything
		"sudo": true,
	}

	cmd := parts[0]
	if idx := strings.LastIndex(cmd, "/"); idx >= 0 {
		cmd = cmd[idx+1:]
	}
	return dangerousTargets[strings.ToLower(cmd)]
}

// containsOutputRedirect checks if the command redirects output to a file.
func containsOutputRedirect(cmdLine string) bool {
	// Check for > or >> outside of quotes
	inSingle := false
	inDouble := false
	for i := 0; i < len(cmdLine); i++ {
		ch := cmdLine[i]
		if ch == '\'' && !inDouble {
			inSingle = !inSingle
		} else if ch == '"' && !inSingle {
			inDouble = !inDouble
		} else if ch == '>' && !inSingle && !inDouble {
			// Check it's not inside a comparison like 2>&1
			if i > 0 && cmdLine[i-1] >= '0' && cmdLine[i-1] <= '9' {
				// fd redirect like 2>&1 — check if target is &
				if i+1 < len(cmdLine) && cmdLine[i+1] == '&' {
					continue // fd redirection, not file redirect
				}
			}
			return true
		}
	}
	return false
}

func containsIgnoreCase(slice []string, item string) bool {
	itemLower := strings.ToLower(item)
	for _, s := range slice {
		if strings.ToLower(s) == itemLower {
			return true
		}
	}
	return false
}
