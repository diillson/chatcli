/*
 * ChatCLI - Tests for mid-loop skill re-activation (skill_rescan.go)
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Exercises the re-scan that closes the "skills only fire on the initial
 * user query" gap in agent/coder mode:
 *   - trigger keywords surfacing only in the ASSISTANT's reasoning
 *   - path globs matching files named inside tool_call args
 *   - per-Run dedup (startup-injected skills never re-fire mid-loop)
 *   - first-wins model/effort hints (mid-loop skills never override startup)
 *   - env kill switch
 *
 * The fixture mirrors skill_pin_test.go: single-file skills in a temp
 * project dir via Manager.SetProjectDir — fully hermetic.
 */
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	llmclient "github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/pkg/persona"
	"go.uber.org/zap"
)

const (
	rescanFixtureHelm = `---
name: helm-authoring
description: guidance for writing Helm charts
triggers:
  - helm chart
effort: high
---
Always template image tags and pin chart apiVersion v2.`

	rescanFixtureGoTest = `---
name: go-test-style
description: table-driven test conventions
paths:
  - "**/*_test.go"
---
Prefer table-driven tests with t.Run subtests.`

	rescanFixtureWithModel = `---
name: perf-audit
description: performance auditing checklist
triggers:
  - profiling
model: opus
effort: max
---
Collect a pprof profile before optimizing.`
)

// newRescanFixture builds an AgentMode whose ChatCLI carries an isolated
// persona manager seeded with the given skills.
func newRescanFixture(t *testing.T, skills map[string]string) *AgentMode {
	t.Helper()
	tmp := t.TempDir()
	skillsDir := filepath.Join(tmp, ".agent", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatalf("mkdir skillsDir: %v", err)
	}
	for name, body := range skills {
		if err := os.WriteFile(filepath.Join(skillsDir, name+".md"), []byte(body), 0o644); err != nil {
			t.Fatalf("write skill %s: %v", name, err)
		}
	}
	mgr := persona.NewManager(zap.NewNop())
	mgr.SetProjectDir(tmp)
	if _, err := mgr.RefreshSkills(); err != nil {
		t.Fatalf("RefreshSkills: %v", err)
	}
	cli := &ChatCLI{personaHandler: &PersonaHandler{manager: mgr, logger: zap.NewNop()}}
	return &AgentMode{cli: cli, logger: zap.NewNop()}
}

func TestRescanSkillsMidLoop_TriggerInAssistantReasoning(t *testing.T) {
	a := newRescanFixture(t, map[string]string{"helm-authoring": rescanFixtureHelm})

	reasoning := "<reasoning>The user wants this deployed; I will write a Helm chart for the service.</reasoning>"
	block, names := a.rescanSkillsMidLoop(reasoning)

	if len(names) != 1 || names[0] != "helm-authoring" {
		t.Fatalf("expected helm-authoring to activate, got %v", names)
	}
	if !strings.Contains(block, "SKILL AUTO-ACTIVATION — MID-TASK") {
		t.Fatalf("block missing mid-task header:\n%s", block)
	}
	if !strings.Contains(block, "pin chart apiVersion v2") {
		t.Fatalf("block missing skill content:\n%s", block)
	}
	if a.skillEffortHint != llmclient.EffortHigh {
		t.Fatalf("effort hint = %q, want high", a.skillEffortHint)
	}

	// Same text again → deduped, nothing new fires.
	block, names = a.rescanSkillsMidLoop(reasoning)
	if block != "" || len(names) != 0 {
		t.Fatalf("dedup failed: second scan returned %v / %q", names, block)
	}
}

func TestRescanSkillsMidLoop_PathGlobFromToolCallArgs(t *testing.T) {
	a := newRescanFixture(t, map[string]string{"go-test-style": rescanFixtureGoTest})

	response := `<reasoning>Fixing the regression.</reasoning>
<tool_call name="@coder" args="{\"cmd\":\"read\",\"file\":\"pkg/foo/bar_test.go\"}" />`
	_, names := a.rescanSkillsMidLoop(response)

	if len(names) != 1 || names[0] != "go-test-style" {
		t.Fatalf("expected go-test-style via path glob, got %v", names)
	}
}

func TestRescanSkillsMidLoop_StartupInjectionSeedsDedup(t *testing.T) {
	a := newRescanFixture(t, map[string]string{"helm-authoring": rescanFixtureHelm})

	// Simulate Run() startup: the skill already fired on the user query and
	// was recorded via noteInjectedSkills.
	skill, err := a.cli.personaHandler.GetManager().GetSkill("helm-authoring")
	if err != nil {
		t.Fatalf("GetSkill: %v", err)
	}
	a.noteInjectedSkills(skill)

	block, names := a.rescanSkillsMidLoop("planning the helm chart layout now")
	if block != "" || len(names) != 0 {
		t.Fatalf("startup-injected skill must not re-fire mid-loop; got %v", names)
	}
}

func TestRescanSkillsMidLoop_HintsAreFirstWins(t *testing.T) {
	a := newRescanFixture(t, map[string]string{"perf-audit": rescanFixtureWithModel})
	a.skillModelHint = "sonnet"
	a.skillEffortHint = llmclient.EffortLow

	_, names := a.rescanSkillsMidLoop("next I will do some profiling of the hot path")
	if len(names) != 1 {
		t.Fatalf("expected perf-audit to activate, got %v", names)
	}
	if a.skillModelHint != "sonnet" {
		t.Fatalf("mid-loop skill must not override startup model hint; got %q", a.skillModelHint)
	}
	if a.skillEffortHint != llmclient.EffortLow {
		t.Fatalf("mid-loop skill must not override startup effort hint; got %q", a.skillEffortHint)
	}
}

func TestRescanSkillsMidLoop_NoMatchReturnsEmpty(t *testing.T) {
	a := newRescanFixture(t, map[string]string{"helm-authoring": rescanFixtureHelm})
	block, names := a.rescanSkillsMidLoop("just reading some source files")
	if block != "" || len(names) != 0 {
		t.Fatalf("no trigger present, expected empty result; got %v / %q", names, block)
	}
}

func TestRescanSkillsMidLoop_EnvKillSwitch(t *testing.T) {
	t.Setenv(skillRescanEnv, "false")
	a := newRescanFixture(t, map[string]string{"helm-authoring": rescanFixtureHelm})
	block, names := a.rescanSkillsMidLoop("writing a helm chart")
	if block != "" || len(names) != 0 {
		t.Fatalf("rescan disabled by env, expected no activation; got %v", names)
	}
}

func TestBuildMidLoopSkillBlock_Empty(t *testing.T) {
	if got := buildMidLoopSkillBlock(nil); got != "" {
		t.Fatalf("empty skill slice must render empty block, got %q", got)
	}
}
