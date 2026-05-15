package providerparity

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadProviders(t *testing.T) {
	dir := t.TempDir()
	src := `package catalog

const (
	ProviderOpenAI          = "OPENAI"
	ProviderOpenAIAssistant = "OPENAI_ASSISTANT"
	ProviderClaudeAI        = "CLAUDEAI"
	ProviderMoonshot        = "MOONSHOT"
)

// Non-provider const should be ignored.
const SomeOther = "X"

// providerLowerCase is exported but does not start with capital P.
const providerLowerCase = "y"
`
	path := filepath.Join(dir, "catalog.go")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := LoadProviders(path)
	if err != nil {
		t.Fatal(err)
	}
	wantVals := []string{"CLAUDEAI", "MOONSHOT", "OPENAI", "OPENAI_ASSISTANT"}
	gotVals := make([]string, len(got))
	for i, p := range got {
		gotVals[i] = p.Value
	}
	if !reflect.DeepEqual(gotVals, wantVals) {
		t.Errorf("providers = %v, want %v", gotVals, wantVals)
	}
}

func TestTouchPoint_Render(t *testing.T) {
	tp := TouchPoint{Pattern: "{Upper} {lower}"}
	if got := tp.Render("MOONSHOT"); got != "MOONSHOT moonshot" {
		t.Errorf("Render(MOONSHOT) = %q", got)
	}
	if got := tp.Render("GITHUB_MODELS"); got != "GITHUB_MODELS github_models" {
		t.Errorf("Render(GITHUB_MODELS) = %q", got)
	}
}

func TestExemptions_IsExempt(t *testing.T) {
	ex := Exemptions{
		"BEDROCK":          {"env.redactor", "cost.cli"},
		"OPENAI_ASSISTANT": {"*"},
	}
	if !ex.IsExempt("BEDROCK", "env.redactor") {
		t.Error("BEDROCK/env.redactor should be exempt")
	}
	if ex.IsExempt("BEDROCK", "manager.factory") {
		t.Error("BEDROCK/manager.factory should NOT be exempt")
	}
	if !ex.IsExempt("OPENAI_ASSISTANT", "manager.factory") {
		t.Error("OPENAI_ASSISTANT/* wildcard should exempt anything")
	}
	if ex.IsExempt("OPENAI", "manager.factory") {
		t.Error("OPENAI with no entry should not be exempt")
	}
}

func TestCheck_MissingProducesViolations(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("MOONSHOT OPENAI"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("OPENAI"), 0o644); err != nil {
		t.Fatal(err)
	}

	providers := []Provider{{Value: "OPENAI"}, {Value: "MOONSHOT"}}
	points := []TouchPoint{
		{ID: "a", Description: "in a", Path: "a.go", Pattern: "{Upper}"},
		{ID: "b", Description: "in b", Path: "b.go", Pattern: "{Upper}"},
	}
	vs, err := Check(dir, providers, points, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 {
		t.Fatalf("expected 1 violation, got %d: %+v", len(vs), vs)
	}
	if vs[0].Provider != "MOONSHOT" || vs[0].TouchPoint != "b" {
		t.Errorf("violation = %+v, want {Provider: MOONSHOT, TouchPoint: b}", vs[0])
	}
}

func TestCheck_ExemptionsSuppressViolations(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte("only OPENAI here"), 0o644); err != nil {
		t.Fatal(err)
	}
	providers := []Provider{{Value: "OPENAI"}, {Value: "BEDROCK"}}
	points := []TouchPoint{{ID: "env", Path: "x.go", Pattern: "{Upper}"}}

	// BEDROCK would violate, but the exemption clears it.
	ex := Exemptions{"BEDROCK": {"env"}}
	vs, err := Check(dir, providers, points, ex)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 0 {
		t.Errorf("expected no violations after exemption, got: %+v", vs)
	}

	// Wildcard exemption.
	ex = Exemptions{"BEDROCK": {"*"}}
	vs, err = Check(dir, providers, points, ex)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 0 {
		t.Errorf("wildcard exemption should clear all, got: %+v", vs)
	}
}

func TestDefaultMatricesAreInternallyConsistent(t *testing.T) {
	tps := DefaultTouchPoints()
	ids := map[string]bool{}
	for _, tp := range tps {
		if tp.ID == "" || tp.Path == "" || tp.Pattern == "" || tp.Description == "" {
			t.Errorf("incomplete touch point: %+v", tp)
		}
		if ids[tp.ID] {
			t.Errorf("duplicate touch-point ID: %s", tp.ID)
		}
		ids[tp.ID] = true
	}

	// Every exemption ID (other than "*") must reference a known touch point.
	ex := DefaultExemptions()
	for provider, exemptIDs := range ex {
		for _, id := range exemptIDs {
			if id == "*" {
				continue
			}
			if !ids[id] {
				t.Errorf("provider %q exempts unknown touch point %q", provider, id)
			}
		}
	}
}
