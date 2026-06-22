/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Output-token reduction wiring: verbosity steering (the universal, cache-safe
 * lever) plus a conservative, opt-in complexity→effort downgrade. The policy
 * logic is keyless and lives in cli/outputpolicy; this file only reads config
 * and bridges to the cli/agent/chat seams.
 */
package cli

import (
	"strings"

	"github.com/diillson/chatcli/cli/outputpolicy"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/utils"
)

// outputVerbosity resolves the active verbosity level from
// CHATCLI_OUTPUT_VERBOSITY (full | concise | minimal). Default: concise — it
// trims ceremony/restatement while preserving substance.
func outputVerbosity() outputpolicy.Verbosity {
	v, _ := outputpolicy.ParseVerbosity(utils.GetEnvOrDefault("CHATCLI_OUTPUT_VERBOSITY", "concise"))
	return v
}

// verbosityDirectiveBlock returns the steering directive prefixed with the
// block separator, or "" when verbosity is full. It is a static string per
// level, so injecting it keeps the system-prompt prefix cacheable.
func verbosityDirectiveBlock() string {
	d := outputVerbosity().Directive()
	if d == "" {
		return ""
	}
	return "\n\n" + d
}

// outputEffortRoutingEnabled reports whether the complexity→effort downgrade is
// active. Opt-in (default off): lowering reasoning effort is a behavior change
// and only benefits reasoning-capable providers, so it ships disabled.
func outputEffortRoutingEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(utils.GetEnvOrDefault("CHATCLI_OUTPUT_EFFORT_ROUTING", "off")), "on")
}

// routeEffortForPrompt right-sizes the reasoning effort for a user prompt. It
// ONLY downgrades to Low for clearly-trivial prompts, and ONLY when no effort
// was already chosen (base == Unset) — so it never overrides a skill's or the
// user's explicit choice, and never raises effort. Returns base unchanged when
// the feature is off or the prompt isn't trivial.
func routeEffortForPrompt(userInput string, base client.SkillEffort) client.SkillEffort {
	if !outputEffortRoutingEnabled() || base != client.EffortUnset {
		return base
	}
	if outputpolicy.Classify(userInput) == outputpolicy.ComplexityTrivial {
		return client.EffortLow
	}
	return base
}
