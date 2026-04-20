/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Phase 1 (#7) — cross-provider reasoning backbone wiring.
 *
 * The actual cross-provider plumbing already exists: skill_hints.go in
 * llm/client maps SkillEffort → Anthropic thinking_budget and OpenAI
 * reasoning_effort. What we add here is the *policy*: when no effort
 * hint is on the wire, decide whether the pipeline should attach one
 * based on the agent type and Quality.Reasoning config.
 */
package quality

import (
	"context"
	"strings"

	"github.com/diillson/chatcli/cli/agent/workers"
	"github.com/diillson/chatcli/llm/client"
)

// applyAutoReasoning attaches an effort hint to ctx when:
//
//  1. The reasoning mode is not "off"; AND
//  2. ctx does not already carry an effort hint (skill_hints, agent.Effort);
//     AND
//  3. Mode is "on" (apply to every agent) OR Mode is "auto" and the agent's
//     type is in cfg.AutoAgents.
//
// The effort tier is derived from cfg.Budget so the user controls how
// expensive "auto reasoning" is via a single number. Budget ≥ 16k → Max,
// ≥ 8k → High, ≥ 4k → Medium, otherwise High (a sane default — auto means
// "give the model room to think", not "spend tokens on nothing").
//
// When the predicates fail, ctx is returned unchanged (zero overhead).
func applyAutoReasoning(ctx context.Context, cfg ReasoningConfig, agent workers.WorkerAgent) context.Context {
	if ctx == nil || agent == nil {
		return ctx
	}
	mode := strings.ToLower(strings.TrimSpace(cfg.Mode))
	if mode == "off" {
		return ctx
	}
	if client.EffortFromContext(ctx) != client.EffortUnset {
		return ctx
	}
	if mode != "on" && !inAutoAgents(string(agent.Type()), cfg.AutoAgents) {
		return ctx
	}
	return client.WithEffortHint(ctx, EffortForBudget(cfg.Budget))
}

// EffortForBudget translates a thinking budget (in tokens) into the
// canonical SkillEffort tier whose ThinkingBudgetForEffort comes closest
// without going under. Used by /thinking budget=N and the auto-enable path.
func EffortForBudget(budget int) client.SkillEffort {
	switch {
	case budget >= 16384:
		return client.EffortMax
	case budget >= 8192:
		return client.EffortHigh
	case budget >= 4096:
		return client.EffortMedium
	default:
		// Auto means "let the model think", not "save pennies". Default
		// to High so reasoning-heavy agents (Planner, Verifier, …) get
		// enough budget to be useful.
		return client.EffortHigh
	}
}

// inAutoAgents reports whether agentType (case-insensitive) appears in the
// list. An empty list means "no agent is auto-enabled" (not "all agents").
func inAutoAgents(agentType string, list []string) bool {
	if len(list) == 0 {
		return false
	}
	target := strings.ToLower(strings.TrimSpace(agentType))
	for _, s := range list {
		if strings.ToLower(strings.TrimSpace(s)) == target {
			return true
		}
	}
	return false
}
