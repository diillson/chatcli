/*
 * ChatCLI - Tests for the per-subcommand /skill suggestion helpers.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Each helper has its own contract — what it suggests, when it returns nil,
 * and how it interacts with the skill registry. These tests use the
 * docWithCursor seam to drive them with realistic Document inputs and
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
// (driven by the registry) would diverge from suggestions backed by the
// persona manager.
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
	projectDir := t.TempDir()
	projectSkills := filepath.Join(projectDir, ".agent", "skills")
	if err := os.MkdirAll(projectSkills, 0o755); err != nil {
		t.Fatalf("mkdir project skills: %v", err)
	}
	for qualifiedName, body := range packages {
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
