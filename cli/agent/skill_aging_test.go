/*
 * ChatCLI - Tests for skill-block aging (skill_aging.go)
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package agent

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/models"
)

// agingHistory builds a history with one mid-loop skill block followed by
// `assistantTurns` assistant messages (each aging the block by one turn).
func agingHistory(skillContent string, skillNames []string, assistantTurns int) []models.Message {
	h := []models.Message{
		{Role: "system", Content: "charter"},
		{Role: "user", Content: "do the task"},
		{Role: "user", Content: skillContent, Meta: &models.MessageMeta{SkillNames: models.JoinSkillNames(skillNames)}},
	}
	for i := 0; i < assistantTurns; i++ {
		h = append(h,
			models.Message{Role: "assistant", Content: "working..."},
			models.Message{Role: "tool", Content: "ok", ToolCallID: "t"},
		)
	}
	return h
}

func TestApplySkillAging_YoungBlockUntouched(t *testing.T) {
	cfg := SkillAgingConfig{TurnsBeforeCollapse: 3}
	content := "[SKILL AUTO-ACTIVATION — MID-TASK]\n\nfull body here"

	for _, turns := range []int{0, 1, 2} {
		h := agingHistory(content, []string{"helm"}, turns)
		_, report := ApplySkillAging(h, cfg, nil)
		if report.Collapsed != 0 {
			t.Fatalf("age %d: collapsed %d blocks, want 0", turns, report.Collapsed)
		}
		if h[2].Content != content {
			t.Fatalf("age %d: content mutated:\n%s", turns, h[2].Content)
		}
	}
}

func TestApplySkillAging_OldBlockCollapses(t *testing.T) {
	cfg := SkillAgingConfig{TurnsBeforeCollapse: 3}
	content := "[SKILL AUTO-ACTIVATION — MID-TASK]\n\n" + strings.Repeat("guidance ", 500)

	h := agingHistory(content, []string{"helm", "gotest"}, 3)
	_, report := ApplySkillAging(h, cfg, nil)

	if report.Collapsed != 1 {
		t.Fatalf("collapsed = %d, want 1", report.Collapsed)
	}
	if got := h[2].Content; !strings.Contains(got, "SKILL GUIDANCE ARCHIVED") {
		t.Fatalf("stub header missing:\n%s", got)
	}
	if !strings.Contains(h[2].Content, "helm, gotest") {
		t.Fatalf("stub must name the collapsed skills:\n%s", h[2].Content)
	}
	if !h[2].Meta.SkillCollapsed {
		t.Fatal("Meta.SkillCollapsed not set")
	}
	if want := []string{"helm", "gotest"}; len(report.CollapsedSkills) != 2 ||
		report.CollapsedSkills[0] != want[0] || report.CollapsedSkills[1] != want[1] {
		t.Fatalf("CollapsedSkills = %v, want %v", report.CollapsedSkills, want)
	}
	if report.CharsSaved <= 0 {
		t.Fatalf("CharsSaved = %d, want > 0", report.CharsSaved)
	}
}

func TestApplySkillAging_AlreadyCollapsedSkipped(t *testing.T) {
	cfg := SkillAgingConfig{TurnsBeforeCollapse: 1}
	h := agingHistory("stub already", []string{"helm"}, 5)
	h[2].Meta.SkillCollapsed = true

	_, report := ApplySkillAging(h, cfg, nil)
	if report.Collapsed != 0 {
		t.Fatalf("re-collapsed an already-collapsed block: %d", report.Collapsed)
	}
	if h[2].Content != "stub already" {
		t.Fatalf("collapsed stub mutated: %q", h[2].Content)
	}
}

func TestApplySkillAging_NonSkillMessagesUntouched(t *testing.T) {
	cfg := SkillAgingConfig{TurnsBeforeCollapse: 1}
	h := []models.Message{
		{Role: "user", Content: "plain user message with no meta"},
		{Role: "user", Content: "summary", Meta: &models.MessageMeta{IsSummary: true}},
		{Role: "assistant", Content: "a"},
		{Role: "assistant", Content: "b"},
	}
	_, report := ApplySkillAging(h, cfg, nil)
	if report.Collapsed != 0 {
		t.Fatalf("collapsed non-skill messages: %d", report.Collapsed)
	}
}

func TestApplySkillAging_NilCCRStillCollapsesWithoutMarker(t *testing.T) {
	cfg := SkillAgingConfig{TurnsBeforeCollapse: 1, CCR: nil}
	h := agingHistory(strings.Repeat("x", 4000), []string{"helm"}, 2)

	_, report := ApplySkillAging(h, cfg, nil)
	if report.Collapsed != 1 {
		t.Fatalf("collapsed = %d, want 1", report.Collapsed)
	}
	if strings.Contains(h[2].Content, "@recall") {
		t.Fatalf("nil CCR must not emit a recall marker:\n%s", h[2].Content)
	}
}

func TestApplySkillAging_EmptyHistory(t *testing.T) {
	got, report := ApplySkillAging(nil, DefaultSkillAgingConfig(), nil)
	if got != nil || report.Collapsed != 0 {
		t.Fatalf("empty history must be a no-op; got %v / %+v", got, report)
	}
}

func TestDefaultSkillAgingConfig_EnvOverride(t *testing.T) {
	t.Setenv(skillAgeTurnsEnvVar, "9")
	if got := DefaultSkillAgingConfig().TurnsBeforeCollapse; got != 9 {
		t.Fatalf("env override = %d, want 9", got)
	}
	t.Setenv(skillAgeTurnsEnvVar, "garbage")
	if got := DefaultSkillAgingConfig().TurnsBeforeCollapse; got != defaultSkillAgeTurns {
		t.Fatalf("garbage env = %d, want default %d", got, defaultSkillAgeTurns)
	}
	t.Setenv(skillAgeTurnsEnvVar, "0")
	if got := DefaultSkillAgingConfig().TurnsBeforeCollapse; got != defaultSkillAgeTurns {
		t.Fatalf("zero env = %d, want default %d (aging cannot be instant)", got, defaultSkillAgeTurns)
	}
}
