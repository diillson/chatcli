/*
 * ChatCLI - Fixture-driven tests for chat_pipeline.go helpers that touch
 * a ChatCLI receiver.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Each test builds the smallest ChatCLI / dependency set needed to exercise
 * one helper. We avoid invoking processLLMRequest end-to-end (it owns
 * goroutines, terminal escapes, animation suppression) and instead poke
 * the individual phases.
 */
package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/persona"
	"go.uber.org/zap"
)

// newPipelineCLI builds a minimal ChatCLI with the personaHandler,
// skillHandler, and logger wired up. Used by tests that drive resolveSkillsForTurn /
// consumePendingManualSkill / pickSkillHints. tmpDir hosts a project skills
// directory that the persona Manager points at.
func newPipelineCLI(t *testing.T, skills map[string]string) (*ChatCLI, *persona.Manager) {
	t.Helper()
	tmp := t.TempDir()
	skillsDir := filepath.Join(tmp, ".agent", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for name, body := range skills {
		if err := os.WriteFile(filepath.Join(skillsDir, name+".md"), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	mgr := persona.NewManager(zap.NewNop())
	mgr.SetProjectDir(tmp)
	if _, err := mgr.RefreshSkills(); err != nil {
		t.Fatalf("RefreshSkills: %v", err)
	}

	ph := &PersonaHandler{manager: mgr, logger: zap.NewNop()}
	sh := NewSkillHandler(zap.NewNop(), mgr)
	cli := &ChatCLI{
		logger:         zap.NewNop(),
		personaHandler: ph,
		skillHandler:   sh,
	}
	return cli, mgr
}

func TestConsumePendingManualSkill_Empty(t *testing.T) {
	cli := &ChatCLI{}
	skill, args := cli.consumePendingManualSkill()
	if skill != nil || args != "" {
		t.Fatalf("empty staging slot should return (nil, \"\"); got (%v, %q)", skill, args)
	}
}

func TestConsumePendingManualSkill_DrainsAndClears(t *testing.T) {
	staged := &persona.Skill{Name: "foo"}
	cli := &ChatCLI{pendingManualSkill: staged, pendingManualSkillArgs: "run X"}

	skill, args := cli.consumePendingManualSkill()
	if skill != staged {
		t.Errorf("returned wrong skill pointer")
	}
	if args != "run X" {
		t.Errorf("args = %q, want 'run X'", args)
	}

	// Calling again must yield empty — slot was cleared.
	skill2, args2 := cli.consumePendingManualSkill()
	if skill2 != nil || args2 != "" {
		t.Errorf("staging slot not cleared after consume; got (%v, %q)", skill2, args2)
	}
}

func TestResolveSkillsForTurn_NilPersonaHandler(t *testing.T) {
	cli := &ChatCLI{}
	pinned, auto, paths := cli.resolveSkillsForTurn("anything", "")
	if pinned != nil || auto != nil || paths != nil {
		t.Errorf("nil personaHandler must short-circuit; got (%v, %v, %v)", pinned, auto, paths)
	}
}

func TestResolveSkillsForTurn_PinnedAndAutoSeparated(t *testing.T) {
	cli, _ := newPipelineCLI(t, map[string]string{
		"alpha": `---
name: alpha
description: trigger on word ` + "`alpha`" + `
triggers: ["alpha"]
---
body
`,
		"bravo": `---
name: bravo
description: pinned skill
---
body
`,
	})
	// Pin bravo. alpha will fire by trigger.
	cli.skillHandler.Pin("bravo")

	pinned, auto, paths := cli.resolveSkillsForTurn("hello alpha world", "")
	if len(pinned) != 1 || pinned[0].Name != "bravo" {
		t.Errorf("expected pinned=[bravo]; got %v", pinned)
	}
	if len(auto) != 1 || auto[0].Name != "alpha" {
		t.Errorf("expected auto=[alpha]; got %v", auto)
	}
	if len(paths) != 0 {
		t.Errorf("no file mentions → paths should be empty; got %v", paths)
	}
}

func TestResolveSkillsForTurn_DedupAutoAgainstPinned(t *testing.T) {
	// Skill 'echo' is both pinned AND a trigger match; it must appear
	// in pinned and be filtered out of auto.
	cli, _ := newPipelineCLI(t, map[string]string{
		"echo": `---
name: echo
description: echoes
triggers: ["echo"]
---
body
`,
	})
	cli.skillHandler.Pin("echo")

	pinned, auto, _ := cli.resolveSkillsForTurn("please echo this", "")
	if len(pinned) != 1 || pinned[0].Name != "echo" {
		t.Errorf("expected pinned=[echo]; got %v", pinned)
	}
	if len(auto) != 0 {
		t.Errorf("expected auto=[] after dedup; got %v", auto)
	}
}

func TestPickSkillHints_NoSkillsReturnsEmpty(t *testing.T) {
	cli := &ChatCLI{logger: zap.NewNop()}
	model, eff := cli.pickSkillHints(nil, nil, nil)
	if model != "" || eff != client.SkillEffort("") {
		t.Errorf("empty input → empty hints; got (%q, %q)", model, eff)
	}
}

func TestPickSkillHints_PinnedWinsTie(t *testing.T) {
	cli := &ChatCLI{logger: zap.NewNop()}
	pinned := []*persona.Skill{{Name: "p", Model: "opus", Effort: "high"}}
	auto := []*persona.Skill{{Name: "a", Model: "sonnet", Effort: "low"}}
	model, eff := cli.pickSkillHints(pinned, auto, nil)
	if model != "opus" {
		t.Errorf("model = %q, want opus", model)
	}
	if eff != client.EffortHigh {
		t.Errorf("effort = %q, want high", eff)
	}
}

func TestPickSkillHints_AutoFillsWhenPinnedEmpty(t *testing.T) {
	cli := &ChatCLI{logger: zap.NewNop()}
	auto := []*persona.Skill{{Name: "a", Model: "sonnet"}}
	model, _ := cli.pickSkillHints(nil, auto, nil)
	if model != "sonnet" {
		t.Errorf("model = %q, want sonnet", model)
	}
}

func TestBuildChatTempHistory_OrderingInvariant(t *testing.T) {
	cli := &ChatCLI{
		history: []models.Message{
			{Role: "user", Content: "u1"},
			{Role: "system", Content: "old-system"},
			{Role: "assistant", Content: "a1"},
		},
	}
	parts := []models.ContentBlock{{Type: "text", Text: "new-system"}}
	out := cli.buildChatTempHistory(parts, "u2", " ctx", nil)

	// Expected: [new-system, old-system, u1, a1, u2+ ctx]
	if len(out) != 5 {
		t.Fatalf("len out = %d, want 5", len(out))
	}
	if out[0].Role != "system" || !containsString(out[0].Content, "new-system") {
		t.Errorf("first must be the new system message; got %+v", out[0])
	}
	if out[1].Role != "system" || out[1].Content != "old-system" {
		t.Errorf("second must be the old system message; got %+v", out[1])
	}
	if out[2].Role != "user" || out[2].Content != "u1" {
		t.Errorf("third must be u1; got %+v", out[2])
	}
	if out[3].Role != "assistant" || out[3].Content != "a1" {
		t.Errorf("fourth must be a1; got %+v", out[3])
	}
	if out[4].Role != "user" || out[4].Content != "u2 ctx" {
		t.Errorf("fifth must be user u2 ctx; got %+v", out[4])
	}
}

func TestBuildChatTempHistory_NoSystemParts(t *testing.T) {
	cli := &ChatCLI{
		history: []models.Message{{Role: "user", Content: "u1"}},
	}
	out := cli.buildChatTempHistory(nil, "u2", "", nil)
	// No parts → no new system message; history is just [u1, u2].
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2", len(out))
	}
	if out[0].Content != "u1" || out[1].Content != "u2" {
		t.Errorf("unexpected history: %+v", out)
	}
}

func TestFireUserPromptSubmitHook_NoManagerIsNoop(t *testing.T) {
	// hookManager is nil — the function has an explicit guard. The
	// invariant under test is "no panic / no nil-deref", and the test
	// framework's implicit "test fails on panic" is the assertion.
	cli := &ChatCLI{}
	cli.fireUserPromptSubmitHook(context.Background(), "hello")
}
