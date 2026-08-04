/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

// sessionSubcommand classifies the invocation for the capability methods
// below without running the full dispatch. Unparseable args yield "" so the
// capability answers fail closed (not read-only, not concurrency-safe).
func sessionSubcommand(args []string) string {
	cmd, _, err := parseSessionInvocation(args)
	if err != nil {
		return ""
	}
	return cmd
}

// IsReadOnly reports true only for the inspecting subcommands. save/fork/
// attach/detach mutate the saved-session store or the live session binding,
// so they are NOT read-only and (in coder mode) go through the policy
// confirmation.
func (*BuiltinSessionPlugin) IsReadOnly(args []string) bool {
	switch sessionSubcommand(args) {
	case "search", "get", "list":
		return true
	default:
		return false
	}
}

// IsConcurrencySafe mirrors IsReadOnly: the read-only lookups can fan out in
// parallel; the binding mutations are serialized by the orchestrator (they
// touch shared ChatCLI state — currentSessionName, history — from the tool
// batch, so serial execution is the concurrency contract).
func (p *BuiltinSessionPlugin) IsConcurrencySafe(args []string) bool {
	return p.IsReadOnly(args)
}
