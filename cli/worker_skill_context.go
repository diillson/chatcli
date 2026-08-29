/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * Worker task context provider: what a dispatched squad/taskgraph worker
 * receives in its system prompt beyond its charter.
 *
 * Historically this was only the proactive recall block (memory/session).
 * This file layers the user's own SKILL.md on top, trigger-matched against
 * the worker's task text — so a worker executing "validate the login page"
 * gets the same curated knowledge the orchestrator would. Pinned skills are
 * always included (explicit user intent); trigger matches are capped and
 * budgeted. Everything is inlined (a worker cannot expand a global skill
 * pointer — validatePath confines its reads to the workspace).
 *
 * 100% cli-side: the registered workers.RegisterWorkerContextProvider hook
 * now points at workerTaskContext, no changes in cli/agent/workers.
 *
 * Note: persona.Skill (user SKILL.md) is unrelated to workers.SkillSet
 * (executable macros) — do not conflate.
 */
package cli

import (
	"strings"

	"github.com/diillson/chatcli/pkg/persona"
)

const (
	// maxWorkerSkills caps trigger-matched skills injected per worker task.
	maxWorkerSkills = 3
	// workerSkillBudget bounds the injected skill characters per worker.
	workerSkillBudget = 8 * 1024
)

// workerTaskContext is the provider registered with the workers package: the
// proactive recall block plus the trigger-matched skill block for one task.
func (a *AgentMode) workerTaskContext(task string) string {
	var parts []string
	if rb := a.followUpRecallBlocks(task); strings.TrimSpace(rb) != "" {
		parts = append(parts, rb)
	}
	if sb := a.workerSkillBlock(task); strings.TrimSpace(sb) != "" {
		parts = append(parts, sb)
	}
	return strings.Join(parts, "\n\n")
}

// workerSkillBlock resolves pinned + trigger-matched skills for the task and
// renders them inline under a task-specific header.
func (a *AgentMode) workerSkillBlock(task string) string {
	if a.cli == nil || a.cli.personaHandler == nil {
		return ""
	}
	mgr := a.cli.personaHandler.GetManager()
	if mgr == nil {
		return ""
	}

	seen := make(map[string]bool)
	var skills []*persona.Skill

	// Pinned skills always reach the worker (explicit user intent), no
	// trigger needed and not subject to the trigger-match cap.
	if a.cli.skillHandler != nil {
		for _, s := range a.cli.skillHandler.GetPinnedSkills() {
			if s != nil && !seen[s.Name] {
				seen[s.Name] = true
				skills = append(skills, s)
			}
		}
	}

	// Trigger + path matches on the task text, capped. Use the pure matchers
	// directly (not FindAutoActivatedSkills) to avoid recording per-worker
	// usage analytics on every parallel dispatch. DisableModelInvocation is
	// already honored by FindTriggeredSkills.
	matched := mgr.FindTriggeredSkills(task)
	matched = append(matched, mgr.FindPathMatchedSkills(extractFilePaths(task))...)
	added := 0
	for _, s := range matched {
		if s == nil || seen[s.Name] || added >= maxWorkerSkills {
			continue
		}
		seen[s.Name] = true
		skills = append(skills, s)
		added++
	}
	if len(skills) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("[SKILL AUTO-ACTIVATION — TASK]\n")
	renderSkillEntriesLimited(&b, skills, workerSkillBudget, false)
	return strings.TrimRight(b.String(), "\n")
}
