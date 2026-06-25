/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/config"
)

// stubMerger folds the improvement into the body deterministically (no LLM) and
// is idempotent: re-applying an improvement already present is a no-op, which
// lets tests exercise the engine's no-churn guarantee.
func stubMerger(_ context.Context, _, currentBody, improvement string) (string, error) {
	cur := strings.TrimSpace(currentBody)
	imp := strings.TrimSpace(improvement)
	if imp == "" || strings.Contains(cur, imp) {
		return cur, nil
	}
	return cur + "\n\n" + imp, nil
}

func TestParseSkillCandidates(t *testing.T) {
	tests := []struct {
		name      string
		response  string
		wantNames []string
		wantBody  string // body of the first candidate, when wantNames non-empty
	}{
		{
			name:      "no section",
			response:  "## LONGTERM\n- user likes Go\n",
			wantNames: nil,
		},
		{
			name:      "nothing new",
			response:  "## SKILL_CANDIDATES\nNOTHING_NEW\n",
			wantNames: nil,
		},
		{
			name: "single block",
			response: "## SKILL_CANDIDATES\n[[skill]]\nname: deploy-this\n" +
				"description: How to deploy project X\ntriggers: deploy, ship it\nbody:\n# Deploy\n\nRun make release\n[[/skill]]\n",
			wantNames: []string{"deploy-this"},
			wantBody:  "# Deploy\n\nRun make release",
		},
		{
			name: "two blocks",
			response: "## SKILL_CANDIDATES\n[[skill]]\nname: alpha-flow\ndescription: a\ntriggers: a\nbody:\nstep a\n[[/skill]]\n" +
				"[[skill]]\nname: beta-flow\ndescription: b\ntriggers: b\nbody:\nstep b\n[[/skill]]\n",
			wantNames: []string{"alpha-flow", "beta-flow"},
			wantBody:  "step a",
		},
		{
			name: "placeholder ignored",
			response: "## SKILL_CANDIDATES\n[[skill]]\nname: kebab-case-slug\ndescription: one line\n" +
				"triggers: a,b\nbody:\nsteps\n[[/skill]]\n",
			wantNames: nil,
		},
		{
			name:      "quoted and uppercased name normalized",
			response:  "[[skill]]\nname: \"Deploy-This\"\ndescription: d\ntriggers: x\nbody:\nb\n[[/skill]]\n",
			wantNames: []string{"deploy-this"},
			wantBody:  "b",
		},
		{
			name:      "missing close tag still parses",
			response:  "[[skill]]\nname: half-open\ndescription: d\ntriggers: x\nbody:\nthe body\n",
			wantNames: []string{"half-open"},
			wantBody:  "the body",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseSkillCandidates(tc.response)
			var names []string
			for _, c := range got {
				names = append(names, c.Name)
			}
			if strings.Join(names, ",") != strings.Join(tc.wantNames, ",") {
				t.Fatalf("names = %v, want %v", names, tc.wantNames)
			}
			if len(tc.wantNames) > 0 && got[0].Body != tc.wantBody {
				t.Fatalf("body = %q, want %q", got[0].Body, tc.wantBody)
			}
		})
	}
}

func TestResolveSelfEvolveMode(t *testing.T) {
	cases := map[string]selfEvolveMode{
		"":         selfEvolveAuto, // default
		"auto":     selfEvolveAuto,
		"AUTO":     selfEvolveAuto,
		"on":       selfEvolveAuto,
		"suggest":  selfEvolveSuggest,
		"propose":  selfEvolveSuggest,
		"off":      selfEvolveOff,
		"disabled": selfEvolveOff,
		"garbage":  selfEvolveAuto, // unknown mirrors the default
	}
	for in, want := range cases {
		t.Setenv(config.SelfEvolveModeEnv, in)
		if in == "" {
			os.Unsetenv(config.SelfEvolveModeEnv)
		}
		if got := resolveSelfEvolveMode(); got != want {
			t.Errorf("mode(%q) = %v, want %v", in, got, want)
		}
	}
}

const candidateResponse = "## SKILL_CANDIDATES\n[[skill]]\nname: deploy-x\n" +
	"description: How to deploy X\ntriggers: deploy x, ship x\nbody:\n# Deploy X\n\nRun make release-x\n[[/skill]]\n"

func skillFile(dir, name string) string {
	return filepath.Join(dir, name, "SKILL.md")
}

func TestApplySkillCandidates_AutoCreatesAndIsReversible(t *testing.T) {
	dir := t.TempDir()
	plugins.SetSkillsDirOverride(dir)
	t.Cleanup(func() { plugins.SetSkillsDirOverride("") })
	cli := newTestCLI()

	// red: off mode must never touch disk.
	if sum := cli.applySkillCandidates(context.Background(), candidateResponse, selfEvolveOff, stubMerger); !sum.isEmpty() {
		t.Fatalf("off mode authored something: %+v", sum)
	}
	if _, err := os.Stat(skillFile(dir, "deploy-x")); !os.IsNotExist(err) {
		t.Fatal("off mode wrote a skill file")
	}

	// green: auto mode authors the skill.
	sum := cli.applySkillCandidates(context.Background(), candidateResponse, selfEvolveAuto, stubMerger)
	if len(sum.Authored) != 1 || sum.Authored[0] != "deploy-x" {
		t.Fatalf("authored = %v, want [deploy-x]", sum.Authored)
	}
	data, err := os.ReadFile(skillFile(dir, "deploy-x"))
	if err != nil {
		t.Fatalf("skill not written: %v", err)
	}
	if !strings.Contains(string(data), "Run make release-x") {
		t.Fatalf("skill body missing, got:\n%s", data)
	}
	// manifest recorded → engine owns it.
	if !loadSelfEvolveManifest().owns("deploy-x", string(data)) {
		t.Fatal("manifest did not record engine ownership")
	}
}

func TestApplySkillCandidates_EvolvesUserSkillWithReversibleBackup(t *testing.T) {
	dir := t.TempDir()
	plugins.SetSkillsDirOverride(dir)
	t.Cleanup(func() { plugins.SetSkillsDirOverride("") })
	cli := newTestCLI()

	// A skill the USER authored by hand (not via the engine; no manifest entry).
	userContent := "---\nname: xpto\ndescription: my xpto skill\n---\n\n# My xpto\n\noriginal step\n"
	if err := os.MkdirAll(filepath.Dir(skillFile(dir, "xpto")), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skillFile(dir, "xpto"), []byte(userContent), 0o600); err != nil {
		t.Fatal(err)
	}

	// The model, now aware xpto exists from the index, emits an EVOLVE block
	// (just the improvement) reusing the exact name — no body in the prompt.
	resp := "## SKILL_CANDIDATES\n[[skill]]\nname: xpto\naction: evolve\n" +
		"improvement:\n- add new improved step\n[[/skill]]\n"
	sum := cli.applySkillCandidates(context.Background(), resp, selfEvolveAuto, stubMerger)

	if len(sum.EvolvedBackup) != 1 || sum.EvolvedBackup[0] != "xpto" {
		t.Fatalf("expected xpto evolved-with-backup, got %+v", sum)
	}
	// The skill now carries the improvement, folded into the original body.
	data, _ := os.ReadFile(skillFile(dir, "xpto"))
	if !strings.Contains(string(data), "add new improved step") {
		t.Fatalf("evolution not applied:\n%s", data)
	}
	if !strings.Contains(string(data), "original step") {
		t.Fatalf("merge dropped the original content:\n%s", data)
	}
	// A backup of the user's original exists.
	if !plugins.HasBackup("xpto") {
		t.Fatal("no backup was taken before evolving a user-authored skill")
	}

	// It is fully reversible.
	restored, err := plugins.RestoreSkill("xpto")
	if err != nil || !restored {
		t.Fatalf("restore failed: restored=%v err=%v", restored, err)
	}
	data, _ = os.ReadFile(skillFile(dir, "xpto"))
	if string(data) != userContent {
		t.Fatalf("restore did not bring back the original:\n%s", data)
	}
	if plugins.HasBackup("xpto") {
		t.Fatal("backup should be consumed after restore")
	}
}

func TestFormatSkillIndex(t *testing.T) {
	// Sorted, names + descriptions only — and crucially NO bodies.
	out := formatSkillIndex(
		[]string{"zeta-skill", "alpha-skill"},
		map[string]string{"zeta-skill": "does zeta", "alpha-skill": "does alpha"},
	)
	if !strings.Contains(out, "alpha-skill — does alpha") || !strings.Contains(out, "zeta-skill — does zeta") {
		t.Fatalf("index missing entries:\n%s", out)
	}
	// Stable order: alpha before zeta.
	if strings.Index(out, "alpha-skill") > strings.Index(out, "zeta-skill") {
		t.Fatalf("index not sorted:\n%s", out)
	}
	if formatSkillIndex(nil, nil) != "" {
		t.Error("empty index should render empty")
	}
}

func TestApplySkillCandidates_EvolvesEngineOwnedSkill(t *testing.T) {
	dir := t.TempDir()
	plugins.SetSkillsDirOverride(dir)
	t.Cleanup(func() { plugins.SetSkillsDirOverride("") })
	cli := newTestCLI()

	cli.applySkillCandidates(context.Background(), candidateResponse, selfEvolveAuto, stubMerger)

	// Same name, new improvement, no user edit → engine evolves in place.
	evolveResp := "[[skill]]\nname: deploy-x\naction: evolve\nimprovement:\n- run make release-x --fast\n[[/skill]]\n"
	sum := cli.applySkillCandidates(context.Background(), evolveResp, selfEvolveAuto, stubMerger)
	if len(sum.Evolved) != 1 || sum.Evolved[0] != "deploy-x" {
		t.Fatalf("evolved = %v, want [deploy-x]", sum.Evolved)
	}
	data, _ := os.ReadFile(skillFile(dir, "deploy-x"))
	if !strings.Contains(string(data), "--fast") {
		t.Fatalf("evolution not applied:\n%s", data)
	}

	// Re-detecting the same improvement (now present) is a no-op.
	sum = cli.applySkillCandidates(context.Background(), evolveResp, selfEvolveAuto, stubMerger)
	if !sum.isEmpty() {
		t.Fatalf("identical re-detection churned the skill: %+v", sum)
	}
}

func TestSkillAuthoringHintGatedByMode(t *testing.T) {
	t.Setenv(config.SelfEvolveModeEnv, "off")
	if h := skillAuthoringHint(); h != "" {
		t.Fatalf("off mode should yield no authoring hint, got: %q", h)
	}
	t.Setenv(config.SelfEvolveModeEnv, "auto")
	h := skillAuthoringHint()
	if !strings.Contains(h, "@skill create") || !strings.Contains(h, "@skill update") {
		t.Fatalf("auto-mode hint should teach @skill create/update, got:\n%s", h)
	}
}

func TestShowConfigSelfEvolveDoesNotPanic(t *testing.T) {
	dir := t.TempDir()
	plugins.SetSkillsDirOverride(dir)
	t.Cleanup(func() { plugins.SetSkillsDirOverride("") })
	// personaHandler nil and an empty manifest must render without panicking.
	newTestCLI().showConfigSelfEvolve()
}

func TestApplySkillCandidates_SuggestModeNeverWrites(t *testing.T) {
	dir := t.TempDir()
	plugins.SetSkillsDirOverride(dir)
	t.Cleanup(func() { plugins.SetSkillsDirOverride("") })
	cli := newTestCLI()

	sum := cli.applySkillCandidates(context.Background(), candidateResponse, selfEvolveSuggest, stubMerger)
	if len(sum.Suggested) != 1 {
		t.Fatalf("suggest mode = %+v, want one suggestion", sum)
	}
	if len(sum.Authored) != 0 || len(sum.Evolved) != 0 {
		t.Fatalf("suggest mode wrote skills: %+v", sum)
	}
	if _, err := os.Stat(skillFile(dir, "deploy-x")); !os.IsNotExist(err) {
		t.Fatal("suggest mode wrote a skill file")
	}
}
