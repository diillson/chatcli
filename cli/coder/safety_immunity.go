/*
 * ChatCLI - Safety Bypass Immunity
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Defines operations that ALWAYS require user confirmation, regardless of
 * any allow rules in the policy. These are "bypass-immune" patterns that
 * protect against catastrophic mistakes.
 *
 * Inspired by openclaude's safety checks that are immune to bypassPermissions mode.
 *
 * Even if a user has "@coder exec" in their allow list, operations matching
 * these patterns will always prompt for confirmation.
 */
package coder

import (
	"regexp"
	"strings"
)

// immunePatterns are compiled regex patterns that ALWAYS require confirmation.
// Order doesn't matter — any match triggers immunity.
var immunePatterns []*regexp.Regexp

func init() {
	patterns := []string{
		// Destructive filesystem operations
		`(?i)\brm\s+(-[a-z]*)?r[a-z]*f`,      // rm -rf, rm -fr, rm --recursive --force
		`(?i)\brm\s+(-[a-z]*)?f[a-z]*r`,      // rm -fr variant
		`(?i)\brm\s+.*\s+/\s*$`,               // rm <anything> /
		`(?i)\bmkfs\b`,                          // format filesystem
		`(?i)\bdd\s+.*of=/dev/`,                // dd write to device
		`(?i)\bshred\b`,                         // secure delete

		// System directories
		`(?i)--file\s+/etc/`,                   // write to /etc
		`(?i)--file\s+/boot/`,                  // write to /boot
		`(?i)--file\s+/sys/`,                   // write to /sys
		`(?i)--file\s+/proc/`,                  // write to /proc
		`(?i)--content.*>\s*/etc/`,             // redirect to /etc

		// Privilege escalation
		`(?i)\bsudo\b`,                          // sudo anything
		`(?i)\bsu\s+-`,                          // su - (switch user)
		`(?i)\bchmod\s+777\b`,                  // world-writable
		`(?i)\bchmod\s+.*\+s\b`,               // setuid/setgid
		`(?i)\bchown\s+root\b`,                 // change owner to root

		// Kernel/system manipulation
		`(?i)\binsmod\b`,                        // load kernel module
		`(?i)\brmmod\b`,                         // remove kernel module
		`(?i)\bmodprobe\b`,                      // module management
		`(?i)\bsysctl\s+-w\b`,                  // write sysctl
		`(?i)\biptables\s+-F\b`,                // flush firewall rules
		`(?i)\bsystemctl\s+(stop|disable|mask)`, // disable services

		// Network exfiltration
		`(?i)/dev/tcp/`,                         // bash reverse shell
		`(?i)\bnc\s+.*-[a-z]*l`,               // netcat listen mode
		`(?i)\bncat\s+.*-[a-z]*l`,             // ncat listen mode

		// Credential access
		`(?i)\.ssh/`,                            // SSH keys directory
		`(?i)\.gnupg/`,                          // GPG keys directory
		`(?i)\.aws/credentials`,                 // AWS credentials
		`(?i)\.kube/config`,                     // Kubernetes config

		// Database destruction
		`(?i)\bdrop\s+(database|table|schema)\b`, // SQL drops
		`(?i)\btruncate\s+table\b`,              // SQL truncate
		`(?i)\bdelete\s+from\b.*where\s+1\s*=\s*1`, // delete all

		// Git force operations
		`(?i)\bgit\s+push\s+.*--force\b`,       // force push
		`(?i)\bgit\s+push\s+.*-f\b`,            // force push short
		`(?i)\bgit\s+reset\s+--hard\b`,          // hard reset
		`(?i)\bgit\s+clean\s+-[a-z]*f`,          // force clean

		// Process killing
		`(?i)\bkill\s+-9\b`,                     // SIGKILL
		`(?i)\bkillall\b`,                       // kill all processes by name
		`(?i)\bpkill\s+-9\b`,                    // process kill SIGKILL

		// Shutdown/reboot
		`(?i)\bshutdown\b`,
		`(?i)\breboot\b`,
		`(?i)\bpoweroff\b`,
		`(?i)\bhalt\b`,
	}

	immunePatterns = make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		compiled, err := regexp.Compile(p)
		if err == nil {
			immunePatterns = append(immunePatterns, compiled)
		}
	}
}

// IsSafetyImmune checks if the given tool command matches any safety bypass
// immunity pattern. If true, the operation MUST always prompt the user,
// regardless of any allow rules in the policy.
func IsSafetyImmune(toolName, args string) bool {
	fullCommand := strings.TrimSpace(toolName + " " + args)
	for _, pattern := range immunePatterns {
		if pattern.MatchString(fullCommand) {
			return true
		}
	}
	return false
}

// GetImmuneReason returns a human-readable reason why the command is immune,
// or empty string if it's not immune.
func GetImmuneReason(toolName, args string) string {
	fullCommand := strings.TrimSpace(toolName + " " + args)
	for _, pattern := range immunePatterns {
		if pattern.MatchString(fullCommand) {
			return "This operation matches a safety-critical pattern and always requires explicit approval."
		}
	}
	return ""
}
