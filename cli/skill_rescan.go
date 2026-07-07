/*
 * ChatCLI - Mid-loop skill re-activation for agent/coder mode
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Skills auto-activate once at Run() start against the user's query. That
 * misses everything the ReAct loop produces afterwards: the model's own
 * <reasoning> text, the file paths its tool calls start touching, and the
 * follow-up instructions the user types mid-session (type-ahead queue or the
 * interactive continuation prompt). This file closes that gap: every turn the
 * loop re-scans that mid-run text against the skill catalog and injects any
 * NEWLY matched skill as an append-only history message — so a skill whose
 * trigger only surfaces in the agent's own plan ("I should write a Helm
 * chart…") still reaches the model in time to shape the very next action.
 *
 * Injection is append-only (a user-role message, like the tool-guard and
 * payload-recovery hints) so the cached system-prompt prefix is never
 * invalidated mid-run. A per-Run dedup set guarantees each skill is injected
 * at most once per session, bounding both token cost and cascade risk.
 */
package cli

import (
	"os"
	"strings"

	llmclient "github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/pkg/persona"
	"go.uber.org/zap"
)

// skillRescanEnv toggles mid-loop skill re-activation. Set to "0" or "false"
// to fall back to the historical behavior (skills only fire on the initial
// user query). On by default — injection is advisory, append-only, and
// bounded by the per-Run dedup set.
const skillRescanEnv = "CHATCLI_AGENT_SKILL_RESCAN"

// skillRescanEnabled reports whether the mid-loop skill re-scan is active.
// Honors CHATCLI_AGENT_SKILL_RESCAN=0|false|off|no.
func skillRescanEnabled() bool {
	switch strings.TrimSpace(strings.ToLower(os.Getenv(skillRescanEnv))) {
	case "0", "false", "off", "no":
		return false
	default:
		return true
	}
}

// rescanNewSkills matches mid-run text (plus any file-path tokens embedded in
// it — tool_call args count) against the skill catalog and returns only the
// skills whose Name is NOT yet in injected. Matched names are recorded in
// injected so a skill fires at most once per agent session.
//
// Pure with respect to AgentMode so tests can drive it with just a
// persona.Manager fixture. A nil injected map means "no dedup state" and is
// treated as empty (nothing recorded — callers must pass a real map to get
// dedup, which AgentMode.rescanSkillsMidLoop guarantees).
func rescanNewSkills(mgr *persona.Manager, injected map[string]bool, text string) []*persona.Skill {
	if mgr == nil || strings.TrimSpace(text) == "" {
		return nil
	}
	matched := mgr.FindAutoActivatedSkills(text, extractFilePaths(text))
	if len(matched) == 0 {
		return nil
	}
	out := make([]*persona.Skill, 0, len(matched))
	for _, s := range matched {
		if injected != nil && injected[s.Name] {
			continue
		}
		if injected != nil {
			injected[s.Name] = true
		}
		out = append(out, s)
	}
	return out
}

// buildMidLoopSkillBlock formats skills that fired DURING the ReAct loop into
// the user-role message injected into history. The header tells the model why
// new instructions appeared mid-task so it treats them as guidance for the
// remaining work, not as a new user request.
func buildMidLoopSkillBlock(skills []*persona.Skill) string {
	if len(skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("[SKILL AUTO-ACTIVATION — MID-TASK]\n\n")
	b.WriteString("The following skills were automatically activated while you were ")
	b.WriteString("working (matched against your own reasoning, the files you are ")
	b.WriteString("touching, or a user follow-up). This is NOT a new user request: ")
	b.WriteString("apply their guidance to the remainder of the CURRENT task, ")
	b.WriteString("adjusting your approach if they suggest a better one.\n\n")
	renderSkillEntries(&b, skills)
	return strings.TrimRight(b.String(), "\n")
}

// rescanSkillsMidLoop scans text produced mid-run (assistant reasoning +
// tool_call args, or a user follow-up) for skills not yet injected in this
// session. Returns the history message content to append ("" when nothing new
// fired) and the activated skill names for user-facing notices.
//
// Skill model/effort hints are honored only when no earlier skill claimed
// them (same first-wins rule as pickSkillModelAndEffort) — a mid-task skill
// must not yank the model out from under a startup skill's explicit choice.
func (a *AgentMode) rescanSkillsMidLoop(text string) (string, []string) {
	if !skillRescanEnabled() || a.cli.personaHandler == nil {
		return "", nil
	}
	mgr := a.cli.personaHandler.GetManager()
	if mgr == nil {
		return "", nil
	}
	if a.injectedSkillNames == nil {
		a.injectedSkillNames = make(map[string]bool)
	}
	fresh := rescanNewSkills(mgr, a.injectedSkillNames, text)
	if len(fresh) == 0 {
		return "", nil
	}

	model, effort, _ := pickSkillModelAndEffort(fresh)
	if model != "" && a.skillModelHint == "" {
		a.skillModelHint = model
	}
	if effort != "" && a.skillEffortHint == llmclient.EffortUnset {
		a.skillEffortHint = llmclient.NormalizeEffort(effort)
	}

	names := make([]string, 0, len(fresh))
	for _, s := range fresh {
		names = append(names, s.Name)
	}
	a.logger.Info("agent mode: mid-loop skill activation",
		zap.Strings("skills", names),
		zap.String("model_hint", a.skillModelHint),
		zap.String("effort_hint", string(a.skillEffortHint)))
	return buildMidLoopSkillBlock(fresh), names
}

// noteInjectedSkills records skill names already delivered to the model (via
// the system-prompt blocks built at Run() start) so the mid-loop re-scan
// never injects a duplicate copy of them.
func (a *AgentMode) noteInjectedSkills(skills ...*persona.Skill) {
	if a.injectedSkillNames == nil {
		a.injectedSkillNames = make(map[string]bool)
	}
	for _, s := range skills {
		if s != nil {
			a.injectedSkillNames[s.Name] = true
		}
	}
}
