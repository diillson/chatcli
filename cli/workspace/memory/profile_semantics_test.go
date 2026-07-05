package memory

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// All fixtures in this file are SYNTHETIC. They mimic the structural shapes
// that broke the old implementation (commas inside parentheses, accent
// variants, dash-separated status suffixes) without carrying any real user
// data — never put personal information in test fixtures.

// --- splitting -------------------------------------------------------------

func TestSplitListItems_ParenAware(t *testing.T) {
	items := splitListItems("Quiz Alpha (Provider Z, 60 questões), Cert-A; Curso Beta (Escola W, módulo 2)")
	want := []string{
		"Quiz Alpha (Provider Z, 60 questões)",
		"Cert-A",
		"Curso Beta (Escola W, módulo 2)",
	}
	if len(items) != len(want) {
		t.Fatalf("expected %d items, got %d: %#v", len(want), len(items), items)
	}
	for i := range want {
		if items[i] != want[i] {
			t.Errorf("item %d: got %q, want %q", i, items[i], want[i])
		}
	}
}

func TestStemOf_CollapsesStatusVariants(t *testing.T) {
	variants := []string{
		"completar avaliação final do Curso Beta (Escola W)",
		"completar avaliação final do Curso Beta (Escola W) — EM PROGRESSO",
		"completar avaliação final do Curso Beta (Escola W) — **EM PROGRESSO** (5/11 respondidas)",
	}
	first := stemOf(variants[0])
	if first == "" {
		t.Fatal("stem must not be empty")
	}
	for _, v := range variants[1:] {
		if stemOf(v) != first {
			t.Errorf("stemOf(%q) = %q, want %q", v, stemOf(v), first)
		}
	}
	if stemOf("publicar um blog pessoal (Hugo, tema claro)") == first {
		t.Error("distinct goals must not share a stem")
	}
	// pt-BR accent/tilde variants of the same entry share a stem.
	if stemOf("aprender violão ~nível 2 (método A)") != stemOf("aprender violao nivel 2") {
		t.Errorf("accent/tilde variants must share a stem: %q vs %q",
			stemOf("aprender violão ~nível 2 (método A)"), stemOf("aprender violao nivel 2"))
	}
}

func TestStemOf_DashIdentityIsPreserved(t *testing.T) {
	// A dash tail is only cut when it LOOKS like a status; dash-separated
	// identity ("Provider Z — quiz alpha") must stay distinct per item.
	a := stemOf("Provider Z — quiz alpha")
	b := stemOf("Provider Z — curso beta")
	if a == b {
		t.Fatalf("dash identity collapsed: %q == %q", a, b)
	}
	if stemOf("Provider Z — quiz alpha — EM PROGRESSO (3/10)") != a {
		t.Error("status tail after dash identity must still be cut")
	}
}

func TestIsFragmentEntry_BothHalvesOfBadSplit(t *testing.T) {
	for _, frag := range []string{
		"60 questões) — EM PROGRESSO (17/60 respondidas)", // right half
		"completar Quiz Alpha (Provider Z",                // left half
	} {
		if !isFragmentEntry(frag) {
			t.Errorf("must detect fragment: %q", frag)
		}
	}
	for _, ok := range []string{
		"publicar um blog pessoal (Hugo, tema claro)",
		"Cert-A",
	} {
		if isFragmentEntry(ok) {
			t.Errorf("must NOT flag balanced entry: %q", ok)
		}
	}
}

// --- upsert (supersede-by-stem) ---------------------------------------------

func TestUpdate_ListUpsertSupersedesByStem(t *testing.T) {
	ps := NewUserProfileStore(t.TempDir(), testLogger())

	ps.Update(map[string]string{"goals": "completar Quiz Alpha (Provider Z, 60 questões) — EM PROGRESSO (8/60)"})
	ps.Update(map[string]string{"goals": "completar Quiz Alpha (Provider Z, 60 questões) — EM PROGRESSO (17/60)"})
	ps.Update(map[string]string{"goals": "publicar um blog pessoal (Hugo, tema claro)"})

	p := ps.Get()
	if len(p.Goals) != 2 {
		t.Fatalf("expected 2 goals (progress superseded), got %d: %#v", len(p.Goals), p.Goals)
	}
	if !strings.Contains(p.Goals[0], "17/60") {
		t.Errorf("expected newest progress to win, got %q", p.Goals[0])
	}
}

// --- replace ----------------------------------------------------------------

func TestUpdate_ListReplaceAndClear(t *testing.T) {
	ps := NewUserProfileStore(t.TempDir(), testLogger())

	ps.Update(map[string]string{"goals": "goal A, goal B, goal C"})
	if changed := ps.Update(map[string]string{"goals_replace": "publicar um blog pessoal (Hugo, tema claro)"}); !changed {
		t.Fatal("replace must report change")
	}
	p := ps.Get()
	if len(p.Goals) != 1 || !strings.Contains(p.Goals[0], "blog") {
		t.Fatalf("expected single replaced goal, got %#v", p.Goals)
	}

	if changed := ps.Update(map[string]string{"goals_replace": ""}); !changed {
		t.Fatal("replace with empty value must clear the list")
	}
	if got := ps.Get().Goals; len(got) != 0 {
		t.Fatalf("expected cleared goals, got %#v", got)
	}
}

// --- remove -----------------------------------------------------------------

func TestUpdate_ListRemoveBySubstring(t *testing.T) {
	ps := NewUserProfileStore(t.TempDir(), testLogger())

	ps.Update(map[string]string{"goals": "obter Certificado Curso Gamma (Provider Z); completar quiz Alpha (Provider Z); publicar um blog pessoal"})
	if changed := ps.Update(map[string]string{"goals_remove": "Provider Z"}); !changed {
		t.Fatal("remove must report change")
	}
	p := ps.Get()
	if len(p.Goals) != 1 || !strings.Contains(p.Goals[0], "blog") {
		t.Fatalf("expected only the blog goal to remain, got %#v", p.Goals)
	}

	// goals_done is an alias for remove.
	ps.Update(map[string]string{"goals": "tirar Cert-C"})
	if changed := ps.Update(map[string]string{"goals_done": "tirar Cert-C"}); !changed {
		t.Fatal("goals_done must remove the completed goal")
	}
	for _, g := range ps.Get().Goals {
		if strings.Contains(g, "Cert-C") {
			t.Fatalf("completed goal still present: %#v", ps.Get().Goals)
		}
	}
	// Removal must be scoped: other fields untouched.
	ps.Update(map[string]string{"certifications": "Cert-A (Provider Z track)"})
	ps.Update(map[string]string{"goals_remove": "Provider Z"})
	if len(ps.Get().Certifications) != 1 {
		t.Fatalf("goals_remove must not touch certifications: %#v", ps.Get().Certifications)
	}
}

// --- new fields: interests, directives, milestones ---------------------------

func TestUpdate_InterestsAndDirectives(t *testing.T) {
	ps := NewUserProfileStore(t.TempDir(), testLogger())

	ps.Update(map[string]string{
		"interests":  "fotografia analógica (Câmera K, lente 50mm), xadrez",
		"directives": "usar exemplos curtos; evitar jargão",
	})
	p := ps.Get()
	if len(p.Interests) != 2 {
		t.Fatalf("expected 2 interests, got %#v", p.Interests)
	}
	if len(p.Directives) != 2 {
		t.Fatalf("expected 2 directives, got %#v", p.Directives)
	}

	prompt := ps.FormatForPrompt()
	for _, want := range []string{"Câmera K", "jargão", "Interests", "Directives"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("FormatForPrompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestUpdate_MilestonesAppendDatedAndDedupe(t *testing.T) {
	ps := NewUserProfileStore(t.TempDir(), testLogger())

	if changed := ps.Update(map[string]string{"milestone": "Concluiu o Curso Gamma (módulos 1, 2 e 3)"}); !changed {
		t.Fatal("milestone must be recorded")
	}
	// Same milestone restated must not duplicate.
	if changed := ps.Update(map[string]string{"milestone": "Concluiu o Curso Gamma (turma 2026)"}); changed {
		t.Fatalf("duplicate milestone (same stem) must be skipped: %#v", ps.Get().Milestones)
	}
	p := ps.Get()
	if len(p.Milestones) != 1 {
		t.Fatalf("expected 1 milestone, got %#v", p.Milestones)
	}
	if p.Milestones[0].Date.IsZero() {
		t.Error("milestone must carry a date")
	}
	if !strings.Contains(ps.FormatForPrompt(), "Milestones") {
		t.Error("FormatForPrompt must include milestones")
	}
}

func TestUpdate_PreferencesRemove(t *testing.T) {
	ps := NewUserProfileStore(t.TempDir(), testLogger())

	ps.Update(map[string]string{"favorite_editor": "editor-x", "shell": "shell-y"})
	if changed := ps.Update(map[string]string{"preferences_remove": "favorite_editor"}); !changed {
		t.Fatal("preferences_remove must report change")
	}
	p := ps.Get()
	if _, ok := p.Preferences["favorite_editor"]; ok {
		t.Error("favorite_editor should have been removed")
	}
	if _, ok := p.Preferences["shell"]; !ok {
		t.Error("shell must be preserved")
	}
}

// --- legacy self-healing on load ---------------------------------------------

func TestLoad_NormalizesLegacyPollutedProfile(t *testing.T) {
	dir := t.TempDir()

	legacy := UserProfile{
		Name: "Fulano de Tal",
		Goals: []string{
			"publicar um blog pessoal (Hugo, tema claro)",
			"completar Quiz Alpha (Provider Z, 60 questões) — EM PROGRESSO (8/60 respondidas)",
			// comma-split fragments (orphan closing / unclosed paren)
			"60 questões) — EM PROGRESSO (17/60 respondidas)",
			"completar Quiz Alpha (Provider Z",
			// stem-duplicate progress variants
			"completar Quiz Alpha (Provider Z, 60 questões) — EM PROGRESSO (53/60 respondidas)",
			// completed goals that never left the list
			"completar quiz Delta (Provider Z — **CONCLUÍDO**)",
			"completar quiz Delta (Provider Z — CONCLUÍDO)",
			// the user's instruction echoed as a goal
			"remove Provider Z certifications from active goals",
			// legitimate active goal mentioning Conclusão (must NOT be dropped)
			"obter Certificado de Conclusão Curso Gamma (Provider Z)",
		},
		Certifications: []string{
			"Cert-A", "cert-a", "Cert-B",
			// dash identity must never collapse into one entry
			"Provider Z — quiz alpha", "Provider Z — curso beta",
		},
	}
	data, err := json.MarshalIndent(legacy, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/user_profile.json", data, 0o600); err != nil {
		t.Fatal(err)
	}

	ps := NewUserProfileStore(dir, testLogger())
	p := ps.Get()

	want := map[string]bool{
		"blog pessoal": true, "Quiz Alpha": true, "Conclusão Curso Gamma": true,
	}
	if len(p.Goals) != len(want) {
		t.Fatalf("expected %d healed goals, got %d: %#v", len(want), len(p.Goals), p.Goals)
	}
	joined := strings.Join(p.Goals, " | ")
	for frag := range want {
		if !strings.Contains(joined, frag) {
			t.Errorf("healed goals missing %q: %#v", frag, p.Goals)
		}
	}
	if !strings.Contains(joined, "53/60") {
		t.Errorf("newest progress variant must win: %#v", p.Goals)
	}
	for _, g := range p.Goals {
		if strings.Contains(strings.ToLower(g), "remove provider z") {
			t.Errorf("instruction echo must be dropped: %q", g)
		}
		if strings.Contains(g, "CONCLUÍDO") {
			t.Errorf("completed goal must be dropped: %q", g)
		}
	}
	// Cert-A/cert-a collapse; Cert-B and both dash-identity entries survive.
	if len(p.Certifications) != 4 {
		t.Errorf("expected 4 healed certifications, got %#v", p.Certifications)
	}

	// The healed profile must be persisted (next run loads it clean).
	raw, err := os.ReadFile(dir + "/user_profile.json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "remove Provider Z certifications") {
		t.Error("healed profile must be persisted to disk")
	}
}
