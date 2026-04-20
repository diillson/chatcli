/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * /thinking — session-level reasoning override.
 *
 * Sits on top of llm/client/skill_hints (which maps SkillEffort to
 * Anthropic thinking_budget and OpenAI reasoning_effort). The override
 * is consumed in cli_llm.go (chat path) and agent_mode.go (orchestrator
 * turn) so the user can force-on, force-off, or pick an explicit tier
 * without touching env vars.
 */
package cli

import (
	"fmt"
	"strings"

	"github.com/diillson/chatcli/cli/agent/quality"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/client"
)

// handleThinkingCommand implements /thinking [on|off|auto|low|medium|high|max|budget=N].
//
// Subcommands:
//   - bare /thinking         → show current override state
//   - /thinking auto         → clear override; defer to skill hints + quality config
//   - /thinking off          → set override; suppress every effort hint for the next turn
//   - /thinking on           → equivalent to /thinking high (intuitive shortcut)
//   - /thinking low|medium|high|max → set override to that tier
//   - /thinking budget=N     → set override to the tier closest to N tokens
func (cli *ChatCLI) handleThinkingCommand(userInput string) {
	parts := strings.Fields(strings.TrimSpace(userInput))
	if len(parts) <= 1 {
		cli.showThinkingState()
		return
	}
	arg := strings.ToLower(strings.TrimSpace(parts[1]))

	switch {
	case arg == "auto":
		cli.thinkingOverride = thinkingOverrideState{}
		fmt.Println(colorize("  "+i18n.T("thinking.cleared"), ColorGreen))
	case arg == "off":
		cli.thinkingOverride = thinkingOverrideState{set: true, effort: client.EffortUnset}
		fmt.Println(colorize("  "+i18n.T("thinking.disabled"), ColorYellow))
	case arg == "on" || arg == "high":
		cli.applyThinkingTier(client.EffortHigh)
	case arg == "low":
		cli.applyThinkingTier(client.EffortLow)
	case arg == "medium" || arg == "med":
		cli.applyThinkingTier(client.EffortMedium)
	case arg == "max" || arg == "maximum":
		cli.applyThinkingTier(client.EffortMax)
	case strings.HasPrefix(arg, "budget="):
		raw := strings.TrimPrefix(arg, "budget=")
		var n int
		if _, err := fmt.Sscanf(raw, "%d", &n); err != nil || n <= 0 {
			fmt.Println(colorize("  "+i18n.T("thinking.bad_budget", raw), ColorYellow))
			return
		}
		cli.applyThinkingTier(quality.EffortForBudget(n))
	default:
		fmt.Println(colorize("  "+i18n.T("thinking.unknown_arg", arg), ColorYellow))
		fmt.Println(colorize("  "+i18n.T("thinking.usage"), ColorGray))
	}
}

// applyThinkingTier sets the override and prints the resulting state in
// one line so the user knows the override is active for next turn.
func (cli *ChatCLI) applyThinkingTier(eff client.SkillEffort) {
	cli.thinkingOverride = thinkingOverrideState{set: true, effort: eff}
	fmt.Println(colorize("  "+i18n.T("thinking.set", string(eff)), ColorGreen))
}

// showThinkingState renders the current override + effective tier so the
// user can see what the next chat turn will send. Without an override the
// effective tier is derived from skill hints + quality.Reasoning.AutoAgents,
// neither of which are known until the call site, so we show "auto" instead.
func (cli *ChatCLI) showThinkingState() {
	if !cli.thinkingOverride.set {
		fmt.Println(colorize("  "+i18n.T("thinking.state_auto"), ColorGray))
		return
	}
	if cli.thinkingOverride.effort == client.EffortUnset {
		fmt.Println(colorize("  "+i18n.T("thinking.state_off"), ColorYellow))
		return
	}
	fmt.Println(colorize("  "+i18n.T("thinking.state_set", string(cli.thinkingOverride.effort)), ColorGreen))
}

// applyThinkingOverride decides the effort hint for the *next LLM call*
// when the user has set /thinking. Returns (effort, true) when the
// override should win, otherwise (skillEffort, false) so the caller can
// fall back to its existing logic.
//
// Used by cli_llm.go (chat) and agent_mode.go (orchestrator turn).
func (cli *ChatCLI) applyThinkingOverride(skillEffort client.SkillEffort) (client.SkillEffort, bool) {
	if !cli.thinkingOverride.set {
		return skillEffort, false
	}
	return cli.thinkingOverride.effort, true
}
