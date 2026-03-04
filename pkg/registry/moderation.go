/*
 * ChatCLI - Skill Moderation
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package registry

import "fmt"

// ShouldBlock returns true if the moderation flags indicate the skill should NOT be installed.
func ShouldBlock(flags ModerationFlags) bool {
	return flags.MalwareDetected || flags.Quarantined
}

// CheckModeration evaluates a SkillMeta and returns a human-readable warning.
// Returns empty string if no moderation flags are raised.
func CheckModeration(meta *SkillMeta) string {
	if meta == nil {
		return ""
	}
	flags := meta.Moderation

	if flags.MalwareDetected {
		msg := fmt.Sprintf("BLOCKED: skill '%s' flagged as MALWARE by %s", meta.Name, meta.RegistryName)
		if flags.Reason != "" {
			msg += fmt.Sprintf(" — %s", flags.Reason)
		}
		return msg
	}

	if flags.Quarantined {
		msg := fmt.Sprintf("BLOCKED: skill '%s' is QUARANTINED by %s", meta.Name, meta.RegistryName)
		if flags.Reason != "" {
			msg += fmt.Sprintf(" — %s", flags.Reason)
		}
		return msg
	}

	if flags.SuspiciousContent {
		msg := fmt.Sprintf("WARNING: skill '%s' flagged as SUSPICIOUS by %s", meta.Name, meta.RegistryName)
		if flags.Reason != "" {
			msg += fmt.Sprintf(" — %s", flags.Reason)
		}
		return msg
	}

	return ""
}

// FormatModerationTag returns a short tag for display in search results.
func FormatModerationTag(flags ModerationFlags) string {
	if flags.MalwareDetected {
		return "BLOCKED"
	}
	if flags.Quarantined {
		return "QUARANTINED"
	}
	if flags.SuspiciousContent {
		return "SUSPICIOUS"
	}
	return ""
}
