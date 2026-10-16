/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

// Capability advertisements for BuiltinMemoryPlugin.

// IsReadOnly reports true for the subcommands that only read the user's
// memory (recall, timeline, map, neighbors). remember, profile and forget
// change it and stay behind the confirmation policy, as does anything that
// does not parse.
func (*BuiltinMemoryPlugin) IsReadOnly(args []string) bool {
	cmd, _, err := parseMemoryInvocation(args)
	if err != nil {
		return false
	}
	switch cmd {
	case "recall", "timeline", "map", "neighbors":
		return true
	default:
		return false
	}
}

// IsConcurrencySafe mirrors IsReadOnly: reads can run alongside other reads.
func (p *BuiltinMemoryPlugin) IsConcurrencySafe(args []string) bool {
	return p.IsReadOnly(args)
}
