/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Per-run context of the agent/coder loop as history messages.
 *
 * The query-driven half of the workspace context (memory retrieval,
 * hint-matched rules) and the skills block (pinned, auto-activated,
 * manual) change with every run. They used to trail the system message
 * uncached, which only avoided paying a cache WRITE for them: the system
 * array sits ahead of every message, so a byte that changed there
 * invalidated the whole cached conversation on each new /coder query —
 * the history had only grown, and all of it was billed again.
 *
 * They now ride as flagged user-role messages appended once per run,
 * right before the query. A history message is append-only: the prefix
 * the previous run cached stays intact and the new blocks are the only
 * write. Unlike the per-turn context (turn_context.go) they are NOT
 * turn-scoped: a turn-scoped provider clears a turn-scoped block at the
 * next user message, and in a tool loop the next user message is the
 * first tool result, while skills and retrieved memory must stay in front
 * of the model for the whole run.
 *
 * The skills message carries Meta.SkillNames, the same shape as a mid-loop
 * injection (skill_rescan.go), so the cross-run curation sees the bodies
 * in the live history and the pressure-gated skill aging can reclaim them
 * when the window fills. The workspace message carries Meta.RunContext so
 * memory extraction, recall and hint building skip it like turn context.
 */
package cli

import (
	"sort"
	"strings"

	"github.com/diillson/chatcli/models"
)

// runContextHeader tells the model what the injected message is. English
// on purpose: it is model-facing, like the turn context header.
const runContextHeader = "[RUN CONTEXT — injected by ChatCLI for this run, not written by the user]\n"

// appendRunContext appends the run's workspace context as a flagged
// history message. A block that would repeat the last run context still in
// history is not appended: the model already read it, and repeating it
// would only grow the window.
func (a *AgentMode) appendRunContext(workspaceTurn string) {
	if a == nil || a.cli == nil {
		return
	}
	text := strings.TrimSpace(workspaceTurn)
	if text == "" {
		return
	}
	full := runContextHeader + text
	if full == lastRunContextText(a.cli.history) {
		return
	}
	a.cli.history = append(a.cli.history, models.RunContextMessage(full))
}

// appendRunSkills appends the run's skills block as a history message
// marked with the names of the skills it carries. Names come from the
// per-run injected set, which the block builders seed before this runs.
// A block identical to the last uncollapsed skills message in history is
// not appended.
func (a *AgentMode) appendRunSkills(skillsText string) {
	if a == nil || a.cli == nil {
		return
	}
	text := strings.TrimSpace(skillsText)
	if text == "" {
		return
	}
	if text == lastRunSkillsText(a.cli.history) {
		return
	}
	names := make([]string, 0, len(a.injectedSkillNames))
	for name := range a.injectedSkillNames {
		names = append(names, name)
	}
	sort.Strings(names)
	a.cli.history = append(a.cli.history, models.Message{
		Role:    "user",
		Content: text,
		Meta:    &models.MessageMeta{SkillNames: models.JoinSkillNames(names)},
	})
}

// lastRunContextText returns the content of the most recent run context
// message in history, or "" when there is none.
func lastRunContextText(history []models.Message) string {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].IsRunContext() {
			return history[i].Content
		}
	}
	return ""
}

// lastRunSkillsText returns the content of the most recent skills message
// in history that has not been collapsed by skill aging, or "".
func lastRunSkillsText(history []models.Message) string {
	for i := len(history) - 1; i >= 0; i-- {
		m := history[i]
		if m.Role == "user" && m.Meta != nil && m.Meta.SkillNames != "" && !m.Meta.SkillCollapsed {
			return m.Content
		}
	}
	return ""
}
