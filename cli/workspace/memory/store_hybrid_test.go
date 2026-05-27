package memory

import (
	"testing"
)

func TestSplitCategoryPrefix(t *testing.T) {
	cases := []struct {
		in       string
		wantCat  string
		wantRest string
	}{
		{"[personal] Earned AWS SAA", "personal", "Earned AWS SAA"},
		{"[gotcha] embed.FS needs '/'", "gotcha", "embed.FS needs '/'"},
		{"[bogus] not a category", "", "[bogus] not a category"},
		{"no prefix at all", "", "no prefix at all"},
		{"[] empty", "", "[] empty"},
	}
	for _, c := range cases {
		gotCat, gotRest := splitCategoryPrefix(c.in)
		if gotCat != c.wantCat || gotRest != c.wantRest {
			t.Errorf("splitCategoryPrefix(%q) = (%q,%q), want (%q,%q)",
				c.in, gotCat, gotRest, c.wantCat, c.wantRest)
		}
	}
}

func TestClassifyFactContent(t *testing.T) {
	cases := map[string]string{
		"User earned the AWS certification":       "personal",
		"User prefers concise answers":            "preference",
		"The architecture uses a plugin system":   "architecture",
		"embed.FS has a gotcha with separators":   "gotcha",
		"This project lives in /Users/foo/repo":   "project",
		"Some unremarkable statement of fact xyz": "general",
	}
	for content, want := range cases {
		if got := classifyFactContent(content); got != want {
			t.Errorf("classifyFactContent(%q) = %q, want %q", content, got, want)
		}
	}
}

func TestAppendLongTerm_Categorizes(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir, DefaultConfig(), testLogger())

	n, err := mgr.appendLongTermCounted("- [gotcha] watch out for nil maps\n- User earned the CKA certification")
	if err != nil {
		t.Fatalf("appendLongTermCounted: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 facts added, got %d", n)
	}

	// Tagged line -> gotcha; untagged personal line -> personal (not general).
	if len(mgr.Facts.GetByCategory("gotcha")) != 1 {
		t.Errorf("expected 1 gotcha fact, got %d", len(mgr.Facts.GetByCategory("gotcha")))
	}
	if len(mgr.Facts.GetByCategory("personal")) != 1 {
		t.Errorf("expected 1 personal fact, got %d", len(mgr.Facts.GetByCategory("personal")))
	}
	if len(mgr.Facts.GetByCategory("general")) != 0 {
		t.Errorf("expected 0 general facts, got %d", len(mgr.Facts.GetByCategory("general")))
	}
}

func TestRememberAndForgetFacts(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir, DefaultConfig(), testLogger())

	if !mgr.RememberFact("User holds the Terraform Associate cert", "personal") {
		t.Fatal("expected first RememberFact to add")
	}
	if mgr.RememberFact("User holds the Terraform Associate cert", "personal") {
		t.Error("expected duplicate RememberFact to be a no-op")
	}

	// Empty category auto-classifies without error.
	if !mgr.RememberFact("User prefers Portuguese", "") {
		t.Error("expected auto-classified fact to be added")
	}

	removed := mgr.ForgetFacts("terraform associate")
	if removed != 1 {
		t.Errorf("expected to forget 1 fact, removed %d", removed)
	}
	if mgr.ForgetFacts("nonexistent-substring") != 0 {
		t.Error("expected 0 removed for non-matching substring")
	}
}

func TestProcessExtraction_Summary(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir, DefaultConfig(), testLogger())

	resp := `## DAILY
- did stuff

## LONGTERM
- [gotcha] something tricky

## PROFILE_UPDATE
certifications=AWS SAA
company=Acme

## TOPICS
Go, Memory

## PROJECTS
project_name=chatcli
project_status=active`

	sum := mgr.ProcessExtraction(resp)
	if sum.IsEmpty() {
		t.Fatal("expected non-empty summary")
	}
	if sum.FactsAdded != 1 {
		t.Errorf("FactsAdded = %d, want 1", sum.FactsAdded)
	}
	if !sum.ProfileUpdated {
		t.Error("expected ProfileUpdated true")
	}
	if sum.TopicsRecorded != 2 {
		t.Errorf("TopicsRecorded = %d, want 2", sum.TopicsRecorded)
	}
	if sum.ProjectsUpserted != 1 {
		t.Errorf("ProjectsUpserted = %d, want 1", sum.ProjectsUpserted)
	}
	if !sum.DailyWritten {
		t.Error("expected DailyWritten true")
	}

	// The free-form profile fields actually landed.
	p := mgr.Profile.Get()
	if p.Company != "Acme" {
		t.Errorf("expected company Acme, got %q", p.Company)
	}
	if len(p.Certifications) != 1 || p.Certifications[0] != "AWS SAA" {
		t.Errorf("expected certification AWS SAA, got %v", p.Certifications)
	}
}

func TestExtractionSummary_IsEmpty(t *testing.T) {
	if !(ExtractionSummary{}).IsEmpty() {
		t.Error("zero summary should be empty")
	}
	if (ExtractionSummary{FactsAdded: 1}).IsEmpty() {
		t.Error("summary with a fact should not be empty")
	}
}
