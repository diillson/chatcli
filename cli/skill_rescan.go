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
	"sort"
	"strings"

	"github.com/diillson/chatcli/cli/agent"
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

// maxSkillsPerMidLoopInjection bounds how many NEW skills a single turn
// boundary may inject. Skills beyond the cap are NOT recorded in the dedup
// set, so they naturally re-candidate on the next boundary — a burst of
// matches drips in at a bounded rate instead of landing as one giant block.
// Deliberately a constant, not an env: the per-block byte budget
// (CHATCLI_SKILL_INJECT_BUDGET) is the tunable lever; this cap only shapes
// the drip cadence.
const maxSkillsPerMidLoopInjection = 3

// rescanNewSkills matches mid-run text (plus any file-path tokens embedded in
// it — tool_call args count) against the skill catalog and returns only the
// skills whose Name is NOT yet in injected, skipping names present in
// cooldown (recently collapsed by skill aging — see releaseCollapsedSkills).
// At most maxNew skills are returned per call (<=0 means unlimited); matched
// names are recorded in injected so a skill fires at most once per agent
// session, but capped-out and cooled-down skills are NOT recorded — they
// re-candidate on a later boundary.
//
// Pure with respect to AgentMode so tests can drive it with just a
// persona.Manager fixture. A nil injected map means "no dedup state" and is
// treated as empty (nothing recorded — callers must pass a real map to get
// dedup, which AgentMode.rescanSkillsMidLoop guarantees).
func rescanNewSkills(mgr *persona.Manager, injected map[string]bool, cooldown map[string]bool, text string, maxNew int) []*persona.Skill {
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
		if cooldown[s.Name] {
			continue
		}
		if maxNew > 0 && len(out) >= maxNew {
			continue
		}
		if injected != nil {
			injected[s.Name] = true
		}
		out = append(out, s)
	}
	return out
}

// skillRunBudget is the cumulative per-Run cap on skill characters injected
// (startup blocks + mid-loop injections). Derived from the per-block budget
// instead of introducing another env: two full blocks' worth of skill bodies
// is plenty for one run, and setting CHATCLI_SKILL_INJECT_BUDGET=0
// (unlimited per block) disables the run cap too, keeping the two knobs
// coherent.
func skillRunBudget() int {
	return 2 * skillInjectBudget()
}

// rescanSkillsMidLoop scans text produced mid-run (assistant reasoning +
// tool_call args, or a user follow-up) for skills not yet injected in this
// session. turn is the agent loop's current turn index, used to hold
// recently-collapsed skills in cooldown so a collapse doesn't ping-pong with
// an immediate re-injection (the trigger word often lingers in the model's
// own reasoning). Returns the history message content to append ("" when
// nothing new fired) and the activated skill names — callers must stamp
// those names into the injected message's Meta.SkillNames so skill aging
// can find the block later.
//
// Skill model/effort hints are honored only when no earlier skill claimed
// them (same first-wins rule as pickSkillModelAndEffort) — a mid-task skill
// must not yank the model out from under a startup skill's explicit choice.
func (a *AgentMode) rescanSkillsMidLoop(text string, turn int) (string, []string) {
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
	cooldownTurns := agent.DefaultSkillAgingConfig().TurnsBeforeCollapse
	var cooldown map[string]bool
	for name, collapsedAt := range a.skillCollapseTurn {
		if turn-collapsedAt < cooldownTurns {
			if cooldown == nil {
				cooldown = make(map[string]bool)
			}
			cooldown[name] = true
		}
	}
	fresh := rescanNewSkills(mgr, a.injectedSkillNames, cooldown, text, maxSkillsPerMidLoopInjection)
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
	block := buildMidLoopSkillBlockBudgeted(fresh, a.skillCharsInjected)
	a.skillCharsInjected += len(block)
	a.logger.Info("agent mode: mid-loop skill activation",
		zap.Strings("skills", names),
		zap.Int("run_skill_chars", a.skillCharsInjected),
		zap.String("model_hint", a.skillModelHint),
		zap.String("effort_hint", string(a.skillEffortHint)))
	return block, names
}

// buildMidLoopSkillBlockBudgeted renders the mid-loop block, degrading every
// body to a read-on-demand pointer once the run has already spent its skill
// budget (skillRunBudget). The activation itself is never suppressed — the
// model must know the skill fired — only the byte cost is bounded.
func buildMidLoopSkillBlockBudgeted(skills []*persona.Skill, alreadySpent int) string {
	if len(skills) == 0 {
		return ""
	}
	runBudget := skillRunBudget()
	deferAll := runBudget > 0 && alreadySpent >= runBudget
	var b strings.Builder
	b.WriteString("[SKILL AUTO-ACTIVATION — MID-TASK]\n\n")
	b.WriteString("The following skills were automatically activated while you were ")
	b.WriteString("working (matched against your own reasoning, the files you are ")
	b.WriteString("touching, or a user follow-up). This is NOT a new user request: ")
	b.WriteString("apply their guidance to the remainder of the CURRENT task, ")
	b.WriteString("adjusting your approach if they suggest a better one.\n\n")
	renderSkillEntriesLimited(&b, skills, skillInjectBudget(), deferAll)
	return strings.TrimRight(b.String(), "\n")
}

// releaseCollapsedSkills removes collapsed skills from the per-Run dedup set
// and stamps the collapse turn, so a skill whose trigger fires again later
// can re-inject fresh — but only after the cooldown window has passed.
func (a *AgentMode) releaseCollapsedSkills(names []string, turn int) {
	if len(names) == 0 {
		return
	}
	if a.skillCollapseTurn == nil {
		a.skillCollapseTurn = make(map[string]int)
	}
	for _, name := range names {
		delete(a.injectedSkillNames, name)
		a.skillCollapseTurn[name] = turn
	}
}

// InjectedSkillNames returns a sorted snapshot of every skill name the
// current Run() has delivered to the model. Consumers: the park snapshot
// and the scheduler adapter (so a job scheduled mid-run inherits the
// creating run's skills).
func (a *AgentMode) InjectedSkillNames() []string {
	if a == nil || len(a.injectedSkillNames) == 0 {
		return nil
	}
	names := make([]string, 0, len(a.injectedSkillNames))
	for name := range a.injectedSkillNames {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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
