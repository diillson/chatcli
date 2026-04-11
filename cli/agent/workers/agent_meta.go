/*
 * ChatCLI - Builtin agent model/effort metadata
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Shared base struct providing Model() and Effort() for built-in workers.
 * Each built-in embeds BuiltinAgentMeta and declares its defaults via
 * NewBuiltinAgentMeta(name, defaultModel, defaultEffort).
 *
 * Runtime override: environment variables CHATCLI_AGENT_<NAME>_MODEL and
 * CHATCLI_AGENT_<NAME>_EFFORT always win over the hardcoded defaults. The
 * `<NAME>` token is the uppercase agent name (e.g. PLANNER, REFACTOR).
 * This gives power users a way to retarget specific workers without
 * recompiling or touching code — useful to dial down cost ("formatter →
 * haiku", "planner → gpt-5") or to experiment with a new model for a
 * single agent.
 */
package workers

import (
	"os"
	"strings"
)

// BuiltinAgentMeta is the embeddable helper every built-in worker uses to
// satisfy the Model()/Effort() slice of the WorkerAgent interface. Custom
// agents (CustomAgent) implement these methods directly from their backing
// persona.Agent fields instead of embedding this struct.
type BuiltinAgentMeta struct {
	// AgentName is the uppercase token used for env-var lookup. Keep it
	// stable across releases — changing the name breaks user overrides.
	AgentName string
	// DefaultModel is the model id this agent prefers when the user
	// hasn't set CHATCLI_AGENT_<NAME>_MODEL. Empty string means "inherit
	// the user's active model" (zero surprise default).
	DefaultModel string
	// DefaultEffort is the effort tier this agent prefers when the user
	// hasn't set CHATCLI_AGENT_<NAME>_EFFORT. Valid values: "low",
	// "medium", "high", "max", or "" (no hint).
	DefaultEffort string
}

// NewBuiltinAgentMeta constructs a BuiltinAgentMeta with the given name and
// defaults. Use this from the package init/constructor so the defaults are
// explicit at the call site (easier to audit than scattered struct
// literals).
func NewBuiltinAgentMeta(name, defaultModel, defaultEffort string) BuiltinAgentMeta {
	return BuiltinAgentMeta{
		AgentName:     strings.ToUpper(name),
		DefaultModel:  defaultModel,
		DefaultEffort: defaultEffort,
	}
}

// Model returns the env override when set, otherwise the hardcoded default.
// The trimmed result is normalized to preserve empty-string semantics.
func (m BuiltinAgentMeta) Model() string {
	if m.AgentName != "" {
		if v := strings.TrimSpace(os.Getenv("CHATCLI_AGENT_" + m.AgentName + "_MODEL")); v != "" {
			return v
		}
	}
	return strings.TrimSpace(m.DefaultModel)
}

// Effort returns the env override when set, otherwise the hardcoded
// default. The result is lowercased so NormalizeEffort downstream always
// gets a canonical string.
func (m BuiltinAgentMeta) Effort() string {
	if m.AgentName != "" {
		if v := strings.TrimSpace(os.Getenv("CHATCLI_AGENT_" + m.AgentName + "_EFFORT")); v != "" {
			return strings.ToLower(v)
		}
	}
	return strings.ToLower(strings.TrimSpace(m.DefaultEffort))
}
