/*
 * ChatCLI - Tests for the per-subcommand /skill suggestion helpers.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Each helper has its own contract — what it suggests, when it returns nil,
 * and how it interacts with the skill registry / pin set. These tests use
 * the docWithCursor seam to drive them with realistic Document inputs and
 * assert on the resulting suggestion slice (membership, ordering, filters).
 */
package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/diillson/chatcli/pkg/persona"
	"go.uber.org/zap"
)

// completerSkillFixture creates a SkillHandler whose registry install dir
// is a temp directory and a persona Manager that knows about the same
// skills. The two sides must agree, otherwise `/skill uninstall` completion
// (driven by the registry) would diverge from `/skill pin` completion
// (driven by the persona manager).
func completerSkillFixture(t *testing.T, packages map[string]string) *ChatCLI {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("CHATCLI_SKILL_INSTALL_DIR", tmp)
	for qualifiedName, body := range packages {
		dir := filepath.Join(tmp, qualifiedName)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	// Persona manager: point at the same dir as a project skills root so
	// the path-based skill loader picks up the same files.
	projectDir := t.TempDir()
	projectSkills := filepath.Join(projectDir, ".agent", "skills")
	if err := os.MkdirAll(projectSkills, 0o755); err != nil {
		t.Fatalf("mkdir project skills: %v", err)
	}
	for qualifiedName, body := range packages {
		// Drop the "source--" prefix when writing into the project dir so
		// the persona loader keys the skill by its frontmatter `name:`.
		filename := qualifiedName + ".md"
		if err := os.WriteFile(filepath.Join(projectSkills, filename), []byte(body), 0o644); err != nil {
			t.Fatalf("write project skill: %v", err)
		}
	}
	mgr := persona.NewManager(zap.NewNop())
	mgr.SetProjectDir(projectDir)
	_, _ = mgr.RefreshSkills()

	return &ChatCLI{
		logger:         zap.NewNop(),
		personaHandler: &PersonaHandler{manager: mgr, logger: zap.NewNop()},
		skillHandler:   NewSkillHandler(zap.NewNop(), mgr),
	}
}

func TestSuggestInstalledSkills_ReturnsAllInstalledNames(t *testing.T) {
	cli := completerSkillFixture(t, map[string]string{
		"local--alpha": "---\nname: alpha\ndescription: a\n---\nbody\n",
		"local--bravo": "---\nname: bravo\ndescription: b\n---\nbody\n",
	})
	d := docWithCursor("/skill uninstall ", len("/skill uninstall "))
	got := cli.suggestInstalledSkills(d)

	seen := map[string]bool{}
	for _, s := range got {
		seen[s.Text] = true
	}
	for _, name := range []string{"local--alpha", "local--bravo"} {
		if !seen[name] {
			t.Errorf("expected installed skill %q in completion, got %v", name, got)
		}
	}
}

func TestSuggestInstalledSkills_NoHandlerReturnsNil(t *testing.T) {
	cli := &ChatCLI{}
	if got := cli.suggestInstalledSkills(docWithCursor("/skill uninstall ", 17)); got != nil {
		t.Errorf("expected nil when skillHandler is missing; got %+v", got)
	}
}

func TestSuggestInstallOrInfoArgs_SuggestsFromFlag(t *testing.T) {
	cli := completerSkillFixture(t, map[string]string{
		"local--alpha": "---\nname: alpha\ndescription: a\n---\nbody\n",
	})
	// "/skill install alpha " — cursor past the skill name → should offer --from.
	line := "/skill install alpha "
	d := docWithCursor(line, len(line))
	got := cli.suggestInstallOrInfoArgs(d)

	hasFromFlag := false
	for _, s := range got {
		if s.Text == "--from" {
			hasFromFlag = true
		}
	}
	if !hasFromFlag {
		t.Errorf("expected --from in suggestions after the skill name; got %+v", got)
	}
}

func TestSuggestInstallOrInfoArgs_NoHandlerReturnsNil(t *testing.T) {
	cli := &ChatCLI{}
	if got := cli.suggestInstallOrInfoArgs(docWithCursor("/skill install ", 15)); got != nil {
		t.Errorf("expected nil when skillHandler missing")
	}
}

func TestSuggestRegistrySubcommand_OffersEnableDisable(t *testing.T) {
	cli := completerSkillFixture(t, nil)
	// "/skill registry " — wants the enable|disable verb.
	d := docWithCursor("/skill registry ", len("/skill registry "))
	got := cli.suggestRegistrySubcommand(d)
	seen := map[string]bool{}
	for _, s := range got {
		seen[s.Text] = true
	}
	if !seen["enable"] || !seen["disable"] {
		t.Errorf("expected enable+disable verbs in suggestions; got %+v", got)
	}
}

func TestSuggestPinCandidates_FiltersAlreadyPinnedAndDisabled(t *testing.T) {
	cli := completerSkillFixture(t, map[string]string{
		"alpha":       "---\nname: alpha\ndescription: pinnable\n---\nbody\n",
		"bravo":       "---\nname: bravo\ndescription: pinnable\n---\nbody\n",
		"manual-only": "---\nname: manual-only\ndescription: must hide\ndisable-model-invocation: true\n---\nbody\n",
	})
	cli.skillHandler.Pin("alpha")

	d := docWithCursor("/skill pin ", len("/skill pin "))
	got := cli.suggestPinCandidates(d)

	seen := map[string]bool{}
	for _, s := range got {
		seen[s.Text] = true
	}
	if seen["alpha"] {
		t.Errorf("already-pinned 'alpha' must NOT be suggested again")
	}
	if seen["manual-only"] {
		t.Errorf("disable-model-invocation skill must NOT be suggested as a pin candidate")
	}
	if !seen["bravo"] {
		t.Errorf("ordinary skill 'bravo' must be suggested as a pin candidate")
	}
}

func TestSuggestPinnedNames_OnlyCurrentlyPinned(t *testing.T) {
	cli := completerSkillFixture(t, map[string]string{
		"alpha": "---\nname: alpha\ndescription: x\n---\nbody\n",
		"bravo": "---\nname: bravo\ndescription: x\n---\nbody\n",
	})
	cli.skillHandler.Pin("alpha")

	d := docWithCursor("/skill unpin ", len("/skill unpin "))
	got := cli.suggestPinnedNames(d)
	seen := map[string]bool{}
	for _, s := range got {
		seen[s.Text] = true
	}
	if !seen["alpha"] {
		t.Errorf("expected pinned 'alpha' in unpin completion")
	}
	if seen["bravo"] {
		t.Errorf("non-pinned 'bravo' must NOT appear in unpin completion")
	}
}

func TestSuggestPinnedNames_EmptyWhenNothingPinned(t *testing.T) {
	cli := completerSkillFixture(t, map[string]string{
		"alpha": "---\nname: alpha\ndescription: x\n---\nbody\n",
	})
	d := docWithCursor("/skill unpin ", len("/skill unpin "))
	got := cli.suggestPinnedNames(d)
	if len(got) != 0 {
		t.Errorf("nothing pinned → empty suggestions; got %+v", got)
	}
}

func TestGetUserInvocableSkillSuggestions_PicksOnlyUserInvocable(t *testing.T) {
	cli := completerSkillFixture(t, map[string]string{
		"hidden":   "---\nname: hidden\ndescription: not invocable\n---\nbody\n",
		"runnable": "---\nname: runnable\ndescription: invocable\nuser-invocable: true\n---\nbody\n",
		"withhint": "---\nname: withhint\ndescription: invocable with hint\nuser-invocable: true\nargument-hint: '<x>'\n---\nbody\n",
	})
	got := cli.getUserInvocableSkillSuggestions()
	seen := map[string]bool{}
	for _, s := range got {
		seen[s.Text] = true
	}
	if seen["/hidden"] {
		t.Errorf("non-invocable 'hidden' must NOT appear in user-invocable list")
	}
	if !seen["/runnable"] {
		t.Errorf("missing user-invocable '/runnable'")
	}
	if !seen["/withhint"] {
		t.Errorf("missing '/withhint'")
	}
	// Verify the argument-hint shows up in the description for /withhint.
	for _, s := range got {
		if s.Text == "/withhint" && s.Description == "" {
			t.Errorf("/withhint should carry a description with the argument hint")
		}
	}
}

func TestGetUserInvocableSkillSuggestions_NilPersonaHandlerReturnsNil(t *testing.T) {
	cli := &ChatCLI{}
	if got := cli.getUserInvocableSkillSuggestions(); got != nil {
		t.Errorf("nil persona handler must return nil; got %+v", got)
	}
}
