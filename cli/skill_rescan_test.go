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
	block, names := a.rescanSkillsMidLoop(reasoning, 0)

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
	block, names = a.rescanSkillsMidLoop(reasoning, 0)
	if block != "" || len(names) != 0 {
		t.Fatalf("dedup failed: second scan returned %v / %q", names, block)
	}
}

func TestRescanSkillsMidLoop_PathGlobFromToolCallArgs(t *testing.T) {
	a := newRescanFixture(t, map[string]string{"go-test-style": rescanFixtureGoTest})

	response := `<reasoning>Fixing the regression.</reasoning>
<tool_call name="@coder" args="{\"cmd\":\"read\",\"file\":\"pkg/foo/bar_test.go\"}" />`
	_, names := a.rescanSkillsMidLoop(response, 0)

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

	block, names := a.rescanSkillsMidLoop("planning the helm chart layout now", 0)
	if block != "" || len(names) != 0 {
		t.Fatalf("startup-injected skill must not re-fire mid-loop; got %v", names)
	}
}

func TestRescanSkillsMidLoop_HintsAreFirstWins(t *testing.T) {
	a := newRescanFixture(t, map[string]string{"perf-audit": rescanFixtureWithModel})
	a.skillModelHint = "sonnet"
	a.skillEffortHint = llmclient.EffortLow

	_, names := a.rescanSkillsMidLoop("next I will do some profiling of the hot path", 0)
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
	block, names := a.rescanSkillsMidLoop("just reading some source files", 0)
	if block != "" || len(names) != 0 {
		t.Fatalf("no trigger present, expected empty result; got %v / %q", names, block)
	}
}

func TestRescanSkillsMidLoop_EnvKillSwitch(t *testing.T) {
	t.Setenv(skillRescanEnv, "false")
	a := newRescanFixture(t, map[string]string{"helm-authoring": rescanFixtureHelm})
	block, names := a.rescanSkillsMidLoop("writing a helm chart", 0)
	if block != "" || len(names) != 0 {
		t.Fatalf("rescan disabled by env, expected no activation; got %v", names)
	}
}

func TestBuildMidLoopSkillBlock_Empty(t *testing.T) {
	if got := buildMidLoopSkillBlockBudgeted(nil, 0); got != "" {
		t.Fatalf("empty skill slice must render empty block, got %q", got)
	}
}

func TestRescanSkillsMidLoop_PerInjectCapDripsWithoutDroppingSkills(t *testing.T) {
	skills := make(map[string]string)
	// Five skills sharing the same trigger — more than the per-inject cap.
	for _, n := range []string{"aaa", "bbb", "ccc", "ddd", "eee"} {
		skills[n] = "---\nname: " + n + "\ndescription: d\ntriggers:\n  - helm chart\n---\nbody-" + n
	}
	a := newRescanFixture(t, skills)

	_, first := a.rescanSkillsMidLoop("writing a helm chart", 0)
	if len(first) != maxSkillsPerMidLoopInjection {
		t.Fatalf("first injection = %d skills, want cap %d", len(first), maxSkillsPerMidLoopInjection)
	}
	// Capped-out skills were NOT recorded as injected: they re-candidate on
	// the next boundary.
	_, second := a.rescanSkillsMidLoop("still writing the helm chart", 1)
	if len(second) != 2 {
		t.Fatalf("second injection = %d skills, want the 2 remaining; got %v", len(second), second)
	}
	seen := make(map[string]bool)
	for _, n := range append(first, second...) {
		if seen[n] {
			t.Fatalf("skill %s injected twice across the drip", n)
		}
		seen[n] = true
	}
	if len(seen) != 5 {
		t.Fatalf("drip lost skills: got %d of 5", len(seen))
	}
}

func TestReleaseCollapsedSkills_CooldownThenRetrigger(t *testing.T) {
	a := newRescanFixture(t, map[string]string{"helm-authoring": rescanFixtureHelm})

	_, names := a.rescanSkillsMidLoop("writing a helm chart", 0)
	if len(names) != 1 {
		t.Fatalf("expected initial activation, got %v", names)
	}

	// Aging collapsed the block at turn 2: released from dedup, cooldown starts.
	a.releaseCollapsedSkills(names, 2)

	// Within the cooldown window: the lingering trigger must NOT re-inject.
	if block, again := a.rescanSkillsMidLoop("the helm chart still needs values", 3); block != "" || len(again) != 0 {
		t.Fatalf("re-injection inside cooldown; got %v", again)
	}
	// After the cooldown (default 6 turns): re-trigger injects fresh.
	if _, again := a.rescanSkillsMidLoop("back to the helm chart", 9); len(again) != 1 || again[0] != "helm-authoring" {
		t.Fatalf("expected re-injection after cooldown, got %v", again)
	}
}

func TestRescanSkillsMidLoop_RunBudgetDegradesToPointer(t *testing.T) {
	a := newRescanFixture(t, map[string]string{"helm-authoring": rescanFixtureHelm})
	// Simulate a run that already spent its whole skill budget.
	a.skillCharsInjected = skillRunBudget() + 1

	block, names := a.rescanSkillsMidLoop("writing a helm chart", 0)
	if len(names) != 1 {
		t.Fatalf("activation must never be suppressed by the run budget; got %v", names)
	}
	if strings.Contains(block, "pin chart apiVersion v2") {
		t.Fatalf("over-budget block must not inline the body:\n%s", block)
	}
	if !strings.Contains(block, "Body not inlined") {
		t.Fatalf("over-budget block must carry the read-on-demand pointer:\n%s", block)
	}
	if !strings.Contains(block, "guidance for writing Helm charts") {
		t.Fatalf("description must always be visible:\n%s", block)
	}
}
