package memory

import (
	"strings"
	"testing"
)

// TestProcessExtraction_GoalLifecycle proves the V3 lifecycle keys flow from a
// raw extraction response through parseEnhancedResponse into the profile:
// completed goals leave the active list, credentials and milestones land, and
// interests/directives are captured.
func TestProcessExtraction_GoalLifecycle(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir, DefaultConfig(), testLogger())

	mgr.Profile.Update(map[string]string{
		"goals": "obter Certificado Curso Gamma (Provider Z), publicar um blog pessoal (Hugo, tema claro)",
	})

	response := `## DAILY
- discussed certification completion

## LONGTERM
NOTHING_NEW

## PROFILE_UPDATE
goals_done=obter Certificado Curso Gamma (Provider Z)
certifications=Certificado Curso Gamma
milestone=Concluiu a certificação do Curso Gamma
interests=fotografia analógica (Câmera K, lente 50mm)
directives=usar exemplos curtos; evitar jargão

## TOPICS
NOTHING_NEW

## PROJECTS
NOTHING_NEW`

	sum := mgr.ProcessExtractionResult(response)
	if !sum.ProfileUpdated {
		t.Fatal("expected profile to be updated")
	}

	p := mgr.Profile.Get()
	if len(p.Goals) != 1 || !strings.Contains(p.Goals[0], "blog") {
		t.Errorf("completed goal must leave the active list, got %#v", p.Goals)
	}
	if len(p.Certifications) != 1 || !strings.Contains(p.Certifications[0], "Curso Gamma") {
		t.Errorf("certification must be recorded, got %#v", p.Certifications)
	}
	if len(p.Milestones) != 1 {
		t.Errorf("milestone must be recorded, got %#v", p.Milestones)
	}
	if len(p.Interests) != 1 || !strings.Contains(p.Interests[0], "Câmera K") {
		t.Errorf("interest with commas inside parens must stay whole, got %#v", p.Interests)
	}
	if len(p.Directives) != 2 {
		t.Errorf("expected 2 directives, got %#v", p.Directives)
	}
}
