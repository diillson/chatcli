package memory

import (
	"strings"
	"testing"
)

func TestUserProfile_TypedAndFreeformFields(t *testing.T) {
	dir := t.TempDir()
	ps := NewUserProfileStore(dir, testLogger())

	changed := ps.Update(map[string]string{
		"company":        "Acme",
		"location":       "São Paulo",
		"certifications": "AWS SAA, CKA", // comma-separated -> two entries
		"skills":         "Go",
		"github":         "diillson", // unknown key -> preference
	})
	if !changed {
		t.Fatal("expected profile to change")
	}

	p := ps.Get()
	if p.Company != "Acme" || p.Location != "São Paulo" {
		t.Errorf("scalar fields wrong: %q / %q", p.Company, p.Location)
	}
	if len(p.Certifications) != 2 {
		t.Errorf("expected 2 certifications, got %v", p.Certifications)
	}
	if p.Preferences["github"] != "diillson" {
		t.Errorf("expected freeform preference preserved, got %v", p.Preferences)
	}

	// Re-adding an existing cert is a no-op; a new one appends.
	if ps.Update(map[string]string{"certifications": "AWS SAA"}) {
		t.Error("re-adding existing cert should not change profile")
	}
	if !ps.Update(map[string]string{"certifications": "Terraform Associate"}) {
		t.Error("adding a new cert should change profile")
	}
	if got := len(ps.Get().Certifications); got != 3 {
		t.Errorf("expected 3 certifications after append, got %d", got)
	}

	// New fields surface in the prompt projection.
	prompt := ps.FormatForPrompt()
	for _, want := range []string{"Acme", "São Paulo", "AWS SAA", "Certifications"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("FormatForPrompt missing %q; got:\n%s", want, prompt)
		}
	}
}

func TestUserProfile_IsEmpty(t *testing.T) {
	dir := t.TempDir()
	ps := NewUserProfileStore(dir, testLogger())
	if !ps.IsEmpty() {
		t.Error("fresh profile should be empty")
	}
	ps.RecordCommand("/memory") // command counts don't count as user data
	if !ps.IsEmpty() {
		t.Error("command counts should not make profile non-empty")
	}
	ps.Update(map[string]string{"skills": "Go"})
	if ps.IsEmpty() {
		t.Error("profile with a skill should not be empty")
	}
}

func TestUpsertItems(t *testing.T) {
	var list []string
	if !upsertItems(&list, splitListItems("a, b ,c")) || len(list) != 3 {
		t.Fatalf("expected 3 items, got %v", list)
	}
	// Case variant shares the stem: it supersedes in place, never appends.
	upsertItems(&list, splitListItems("A"))
	if len(list) != 3 {
		t.Errorf("expected case-insensitive stem dedup to keep 3 items, got %v", list)
	}
	if !upsertItems(&list, splitListItems("d")) || len(list) != 4 {
		t.Errorf("expected 4 items after adding d, got %v", list)
	}
	// Identical restatement is a no-op.
	if upsertItems(&list, splitListItems("d")) {
		t.Error("identical item must not report change")
	}
}
