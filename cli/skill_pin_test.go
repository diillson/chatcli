/*
 * ChatCLI - Tests for `/skill pin|unpin|pinned`
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Exercises the session-scoped pin set added to SkillHandler:
 *   - Pin validation (skill missing / disable-model-invocation / already pinned)
 *   - Unpin success + no-op path
 *   - IsPinned / PinnedNames accessors
 *   - GetPinnedSkills lazy stale pruning
 *
 * The fixture writes single-file skills to a temp project directory and
 * points the persona Manager at it via SetProjectDir, so the tests run
 * fully hermetically — no global ~/.chatcli files are touched and the
 * registry config is allowed to fall back to defaults.
 */
package cli

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/diillson/chatcli/pkg/persona"
	"go.uber.org/zap"
)

// newPinTestFixture builds a SkillHandler with an isolated persona manager
// pointing at tmp/.agent/skills. Skills are seeded by writing one
// frontmatter-only .md file per entry. Returns the handler and the
// underlying manager so individual tests can re-list skills if needed.
func newPinTestFixture(t *testing.T, skills map[string]string) (*SkillHandler, *persona.Manager) {
	t.Helper()
	tmp := t.TempDir()
	skillsDir := filepath.Join(tmp, ".agent", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatalf("mkdir skillsDir: %v", err)
	}
	for name, body := range skills {
		path := filepath.Join(skillsDir, name+".md")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write skill %s: %v", name, err)
		}
	}

	mgr := persona.NewManager(zap.NewNop())
	mgr.SetProjectDir(tmp)

	// Force a list pass so the loader caches everything we just wrote;
	// otherwise the first GetSkillByName lookup might miss on slower disks.
	if _, err := mgr.RefreshSkills(); err != nil {
		t.Fatalf("RefreshSkills: %v", err)
	}

	sh := NewSkillHandler(zap.NewNop(), mgr)
	return sh, mgr
}

const (
	pinFixtureNormal = `---
name: pin-normal
description: ordinary pinnable skill
---
body content`

	pinFixtureManualOnly = `---
name: pin-manual-only
description: must not be pinnable
disable-model-invocation: true
---
body`

	pinFixtureWithModel = `---
name: pin-with-model
description: carries a model hint
model: opus
effort: high
---
body`
)

func TestSkillHandlerPin_Success(t *testing.T) {
	sh, _ := newPinTestFixture(t, map[string]string{
		"pin-normal": pinFixtureNormal,
	})
	if sh.IsPinned("pin-normal") {
		t.Fatal("pre-condition: skill should not be pinned yet")
	}

	sh.Pin("pin-normal")

	if !sh.IsPinned("pin-normal") {
		t.Fatalf("expected pin-normal to be pinned, set=%v", sh.PinnedNames())
	}
	if got := sh.PinnedNames(); len(got) != 1 || got[0] != "pin-normal" {
		t.Fatalf("PinnedNames = %v, want [pin-normal]", got)
	}
}

func TestSkillHandlerPin_Idempotent(t *testing.T) {
	sh, _ := newPinTestFixture(t, map[string]string{
		"pin-normal": pinFixtureNormal,
	})
	sh.Pin("pin-normal")
	sh.Pin("pin-normal") // already pinned — should be a silent no-op

	if got := sh.PinnedNames(); len(got) != 1 {
		t.Fatalf("idempotent pin should not duplicate; got %v", got)
	}
}

func TestSkillHandlerPin_DisabledModelInvocation(t *testing.T) {
	sh, _ := newPinTestFixture(t, map[string]string{
		"pin-manual-only": pinFixtureManualOnly,
	})
	sh.Pin("pin-manual-only")

	if sh.IsPinned("pin-manual-only") {
		t.Fatal("skills with disable-model-invocation must not be pinnable")
	}
}

func TestSkillHandlerPin_NotFound(t *testing.T) {
	sh, _ := newPinTestFixture(t, map[string]string{
		"pin-normal": pinFixtureNormal,
	})
	sh.Pin("does-not-exist")

	if sh.IsPinned("does-not-exist") {
		t.Fatal("missing skill must not be added to the pin set")
	}
}

func TestSkillHandlerPin_NoPersonaManager(t *testing.T) {
	// Construct directly without a manager — the early-return guard should
	// keep Pin from panicking.
	sh := &SkillHandler{
		logger:           zap.NewNop(),
		pinnedSkillNames: make(map[string]struct{}),
	}
	sh.Pin("anything") // must not panic, must not pin

	if sh.IsPinned("anything") {
		t.Fatal("Pin without persona manager should reject silently")
	}
}

func TestSkillHandlerUnpin_Success(t *testing.T) {
	sh, _ := newPinTestFixture(t, map[string]string{
		"pin-normal": pinFixtureNormal,
	})
	sh.Pin("pin-normal")
	if !sh.IsPinned("pin-normal") {
		t.Fatal("pre-condition failed")
	}

	sh.Unpin("pin-normal")
	if sh.IsPinned("pin-normal") {
		t.Fatal("Unpin did not remove the skill")
	}
}

func TestSkillHandlerUnpin_NotPinned(t *testing.T) {
	sh, _ := newPinTestFixture(t, map[string]string{
		"pin-normal": pinFixtureNormal,
	})
	sh.Unpin("pin-normal") // never pinned in the first place — must not crash

	if sh.IsPinned("pin-normal") {
		t.Fatal("unpinning unknown skill should not paradoxically register it")
	}
}

func TestSkillHandlerShowPinned_EmptyAndPopulated(t *testing.T) {
	// We can't easily assert stdout here without plumbing a writer through
	// SkillHandler, so we settle for: ShowPinned must never panic on empty
	// or populated state. The real coverage of formatting comes from
	// TestBuildPinnedSkillInjectionBlock.
	sh, _ := newPinTestFixture(t, map[string]string{
		"pin-normal": pinFixtureNormal,
	})
	sh.ShowPinned() // empty
	sh.Pin("pin-normal")
	sh.ShowPinned() // populated
}

func TestSkillHandlerPinnedNames_AlphabeticalOrder(t *testing.T) {
	sh, _ := newPinTestFixture(t, map[string]string{
		"alpha":   makeSkill("alpha", false),
		"bravo":   makeSkill("bravo", false),
		"charlie": makeSkill("charlie", false),
	})
	// Pin out of order
	sh.Pin("charlie")
	sh.Pin("alpha")
	sh.Pin("bravo")

	got := sh.PinnedNames()
	want := []string{"alpha", "bravo", "charlie"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d. got=%v", len(got), len(want), got)
	}
	if !sort.StringsAreSorted(got) {
		t.Fatalf("PinnedNames must be alphabetically sorted for cache stability; got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("PinnedNames[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSkillHandlerGetPinnedSkills_Success(t *testing.T) {
	sh, _ := newPinTestFixture(t, map[string]string{
		"alpha":   makeSkill("alpha", false),
		"bravo":   makeSkill("bravo", false),
		"charlie": makeSkill("charlie", false),
	})
	sh.Pin("charlie")
	sh.Pin("alpha")

	skills := sh.GetPinnedSkills()
	if len(skills) != 2 {
		t.Fatalf("len = %d, want 2", len(skills))
	}
	if skills[0].Name != "alpha" || skills[1].Name != "charlie" {
		t.Fatalf("expected alphabetical [alpha, charlie]; got [%s, %s]",
			skills[0].Name, skills[1].Name)
	}
}

func TestSkillHandlerGetPinnedSkills_StalePruning(t *testing.T) {
	// Seed with two skills, pin both, then delete one from disk and
	// refresh — GetPinnedSkills should silently drop the missing one.
	tmp := t.TempDir()
	skillsDir := filepath.Join(tmp, ".agent", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	alphaPath := filepath.Join(skillsDir, "alpha.md")
	bravoPath := filepath.Join(skillsDir, "bravo.md")
	if err := os.WriteFile(alphaPath, []byte(makeSkill("alpha", false)), 0o644); err != nil {
		t.Fatalf("write alpha: %v", err)
	}
	if err := os.WriteFile(bravoPath, []byte(makeSkill("bravo", false)), 0o644); err != nil {
		t.Fatalf("write bravo: %v", err)
	}
	mgr := persona.NewManager(zap.NewNop())
	mgr.SetProjectDir(tmp)
	if _, err := mgr.RefreshSkills(); err != nil {
		t.Fatalf("RefreshSkills: %v", err)
	}
	sh := NewSkillHandler(zap.NewNop(), mgr)

	sh.Pin("alpha")
	sh.Pin("bravo")
	if len(sh.PinnedNames()) != 2 {
		t.Fatal("pre-condition: both must be pinned")
	}

	// Remove one skill from disk.
	if err := os.Remove(bravoPath); err != nil {
		t.Fatalf("remove bravo: %v", err)
	}

	skills := sh.GetPinnedSkills()
	if len(skills) != 1 {
		t.Fatalf("after stale prune: len = %d, want 1 (got %v)", len(skills), skills)
	}
	if skills[0].Name != "alpha" {
		t.Fatalf("survivor = %q, want alpha", skills[0].Name)
	}
	// The pin set should also be pruned, not just the returned slice.
	if got := sh.PinnedNames(); len(got) != 1 || got[0] != "alpha" {
		t.Fatalf("stale entry was returned but not pruned from set: %v", got)
	}
}

func TestSkillHandlerGetPinnedSkills_NoManager(t *testing.T) {
	sh := &SkillHandler{
		logger:           zap.NewNop(),
		pinnedSkillNames: map[string]struct{}{"x": {}},
	}
	if got := sh.GetPinnedSkills(); got != nil {
		t.Fatalf("GetPinnedSkills with nil manager should return nil; got %v", got)
	}
}

func TestSkillHandlerPin_PreservesModelEffortHints(t *testing.T) {
	// When the pinned skill carries model/effort hints, GetPinnedSkills
	// must return the populated Skill struct so the caller can feed it
	// to pickSkillModelAndEffort.
	sh, _ := newPinTestFixture(t, map[string]string{
		"pin-with-model": pinFixtureWithModel,
	})
	sh.Pin("pin-with-model")
	skills := sh.GetPinnedSkills()
	if len(skills) != 1 {
		t.Fatalf("len = %d, want 1", len(skills))
	}
	if skills[0].Model != "opus" {
		t.Errorf("Model hint not preserved: got %q want opus", skills[0].Model)
	}
	if skills[0].Effort != "high" {
		t.Errorf("Effort hint not preserved: got %q want high", skills[0].Effort)
	}
}

// makeSkill returns the minimal frontmatter+body for a test skill. When
// `manualOnly` is true, `disable-model-invocation` is set so the skill
// becomes unpinnable.
func makeSkill(name string, manualOnly bool) string {
	out := "---\nname: " + name + "\ndescription: test skill\n"
	if manualOnly {
		out += "disable-model-invocation: true\n"
	}
	out += "---\nbody\n"
	return out
}
