package memory

import (
	"strings"
	"testing"
)

func TestUserProfile_Update(t *testing.T) {
	dir := t.TempDir()
	ps := NewUserProfileStore(dir, testLogger())

	changed := ps.Update(map[string]string{
		"name":               "Edilson",
		"role":               "Software Engineer",
		"expertise_level":    "expert",
		"preferred_language": "Portuguese",
	})

	if !changed {
		t.Error("expected profile to be changed")
	}

	p := ps.Get()
	if p.Name != "Edilson" {
		t.Errorf("expected name Edilson, got %q", p.Name)
	}
	if p.Role != "Software Engineer" {
		t.Errorf("expected role, got %q", p.Role)
	}
	if p.ExpertiseLevel != "expert" {
		t.Errorf("expected expert, got %q", p.ExpertiseLevel)
	}
}

func TestUserProfile_RecordCommand(t *testing.T) {
	dir := t.TempDir()
	ps := NewUserProfileStore(dir, testLogger())

	ps.RecordCommand("/memory")
	ps.RecordCommand("/memory")
	ps.RecordCommand("/compact")

	p := ps.Get()
	if p.TopCommands["/memory"] != 2 {
		t.Errorf("expected /memory count 2, got %d", p.TopCommands["/memory"])
	}
}

func TestUserProfile_Persistence(t *testing.T) {
	dir := t.TempDir()
	ps := NewUserProfileStore(dir, testLogger())

	ps.Update(map[string]string{"name": "Test User"})

	ps2 := NewUserProfileStore(dir, testLogger())
	p := ps2.Get()
	if p.Name != "Test User" {
		t.Errorf("expected persisted name, got %q", p.Name)
	}
}

func TestUserProfile_FormatForPrompt(t *testing.T) {
	dir := t.TempDir()
	ps := NewUserProfileStore(dir, testLogger())

	// Empty profile
	if ps.FormatForPrompt() != "" {
		t.Error("expected empty prompt for empty profile")
	}

	ps.Update(map[string]string{
		"name": "Edilson",
		"role": "SRE",
	})

	prompt := ps.FormatForPrompt()
	if !strings.Contains(prompt, "Edilson") || !strings.Contains(prompt, "SRE") {
		t.Errorf("expected name and role in prompt, got %q", prompt)
	}
}

func TestNormalizeExpertise(t *testing.T) {
	cases := map[string]string{
		"beginner":      "beginner",
		"novice":        "beginner",
		"iniciante":     "beginner",
		"intermediate":  "intermediate",
		"intermediario": "intermediate",
		"expert":        "expert",
		"senior":        "expert",
		"avançado":      "expert",
	}
	for input, expected := range cases {
		got := normalizeExpertise(input)
		if got != expected {
			t.Errorf("normalizeExpertise(%q) = %q, want %q", input, got, expected)
		}
	}
}
