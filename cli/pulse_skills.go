/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Live telemetry for skills. A skill counts as activated when it is actually
 * delivered to the model — after trigger matching, dedup and cooldown — not
 * when its trigger merely matched. Each skill is one node on the dashboard;
 * an activation fires it, and aging out of the window marks it collapsed.
 * Only the skill name and how it got there leave the process.
 */
package cli

import (
	"github.com/diillson/chatcli/pkg/persona"
	"github.com/diillson/chatcli/pkg/pulse"
)

// Where a skill activation came from.
const (
	pulseSkillSourceStartup = "run-start"
	pulseSkillSourceMidLoop = "mid-loop"
	pulseSkillSourceChat    = "chat-turn"
)

// Turn nodes: one per surface that talks to the model outside an agent run.
const (
	pulseTurnChat    = "chat"
	pulseTurnOneShot = "one-shot"
)

// pulseSkillsActivated reports skills delivered to the model. parent is the
// run they were delivered to, or empty for a chat turn.
func pulseSkillsActivated(parent, source string, names ...string) {
	if !pulse.Enabled() {
		return
	}
	for _, name := range names {
		if name == "" {
			continue
		}
		pulse.Point(pulse.KindSkill, name, parent, pulse.StatusOK, map[string]string{"state": "active", "source": source})
	}
}

// pulseSkillsCollapsed reports skills whose body aged out of the window.
// It is a state change, not an activation, so it does not count as a call.
func pulseSkillsCollapsed(parent string, names []string) {
	if !pulse.Enabled() {
		return
	}
	for _, name := range names {
		ev := pulse.Event{Kind: pulse.KindSkill, Phase: pulse.PhaseUpdate, ID: "skill:" + name, Parent: parent, Name: name, Status: pulse.StatusOK}
		pulse.Emit(ev.With("state", "collapsed"))
	}
}

// skillNames lists the names of the non-nil skills.
func skillNames(skills []*persona.Skill) []string {
	names := make([]string, 0, len(skills))
	for _, s := range skills {
		if s != nil {
			names = append(names, s.Name)
		}
	}
	return names
}
