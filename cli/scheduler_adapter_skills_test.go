/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"encoding/json"
	"testing"
)

// TestWithDefaultSkillsInjectsActiveSkills pins the inheritance default: a
// schedule_job input without a skills field gains the creating run's
// injected skills.
func TestWithDefaultSkillsInjectsActiveSkills(t *testing.T) {
	cli := &ChatCLI{agentMode: &AgentMode{injectedSkillNames: map[string]bool{
		"database-design": true,
		"clean-code":      true,
	}}}
	a := &schedulerPluginAdapter{cli: cli}

	out := a.withDefaultSkills(`{"name":"j","when":"+1m","do":"agent: x"}`)
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("output must stay valid JSON: %v", err)
	}
	skills, _ := m["skills"].([]any)
	if len(skills) != 2 || skills[0] != "clean-code" || skills[1] != "database-design" {
		t.Errorf("skills = %v, want sorted active skill names", m["skills"])
	}
}

// TestWithDefaultSkillsRespectsExplicitList pins that a caller-provided
// skills field — even an empty one — is never overridden.
func TestWithDefaultSkillsRespectsExplicitList(t *testing.T) {
	cli := &ChatCLI{agentMode: &AgentMode{injectedSkillNames: map[string]bool{"clean-code": true}}}
	a := &schedulerPluginAdapter{cli: cli}

	in := `{"name":"j","skills":[]}`
	if out := a.withDefaultSkills(in); out != in {
		t.Errorf("explicit skills list must pass through untouched, got %s", out)
	}
}

// TestWithDefaultSkillsNoActiveSkillsIsNoop pins the quiet path: nothing
// active means the input is untouched (no empty skills field added).
func TestWithDefaultSkillsNoActiveSkillsIsNoop(t *testing.T) {
	a := &schedulerPluginAdapter{cli: &ChatCLI{}}
	in := `{"name":"j"}`
	if out := a.withDefaultSkills(in); out != in {
		t.Errorf("no active skills must be a no-op, got %s", out)
	}
	if out := a.withDefaultSkills("not json"); out != "not json" {
		t.Error("unparseable input must pass through for downstream error reporting")
	}
}
