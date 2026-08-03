/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package scheduler

import (
	"testing"
)

// TestBuildJobFromInputCarriesSkills pins skill propagation into the
// agent_task payload: names ride the job as JSON-safe payload data so the
// executor (possibly a daemon process) re-resolves them at fire time.
func TestBuildJobFromInputCarriesSkills(t *testing.T) {
	in := &ToolInput{
		Name:   "card-followup",
		When:   "+1m",
		Do:     "agent: revisar o card",
		Skills: []string{"clean-code", "database-design"},
	}
	job, err := buildJobFromInput(in, Owner{Kind: OwnerAgent, ID: "orchestrator"})
	if err != nil {
		t.Fatalf("buildJobFromInput: %v", err)
	}
	if job.Action.Type != ActionAgentTask {
		t.Fatalf("expected agent_task action, got %s", job.Action.Type)
	}
	got := job.Action.PayloadStringSlice("skills")
	if len(got) != 2 || got[0] != "clean-code" || got[1] != "database-design" {
		t.Errorf("payload skills = %v, want the input names in order", got)
	}
}

// TestBuildJobFromInputSkillsOnlyForAgentTask pins the scoping rule: skill
// names are meaningless for shell/webhook actions and must not leak into
// their payloads.
func TestBuildJobFromInputSkillsOnlyForAgentTask(t *testing.T) {
	in := &ToolInput{
		Name:   "cleanup",
		When:   "+1m",
		Do:     "shell: echo done",
		Skills: []string{"clean-code"},
	}
	job, err := buildJobFromInput(in, Owner{Kind: OwnerUser, ID: "u"})
	if err != nil {
		t.Fatalf("buildJobFromInput: %v", err)
	}
	if got := job.Action.PayloadStringSlice("skills"); len(got) != 0 {
		t.Errorf("non-agent_task action must not carry skills, got %v", got)
	}
}
