/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package memory

import (
	"strings"
	"testing"

	"go.uber.org/zap"
)

func newTestFactIndex(t *testing.T) *FactIndex {
	t.Helper()
	return NewFactIndex(t.TempDir(), DefaultConfig(), zap.NewNop())
}

func factByContent(facts []*Fact, substr string) *Fact {
	for _, f := range facts {
		if strings.Contains(f.Content, substr) {
			return f
		}
	}
	return nil
}

func TestConfidenceWeightsRanking(t *testing.T) {
	fi := newTestFactIndex(t)
	// Two unrelated facts of equal recency; only confidence differs.
	fi.AddFactWithMeta("prefers dark theme everywhere", "preference", nil, "", ConfidenceUser, ProvenanceUser)
	fi.AddFactWithMeta("likes verbose logging output", "preference", nil, "", 0.2, ProvenanceExtraction)

	all := fi.GetAll()
	if len(all) != 2 {
		t.Fatalf("expected 2 facts, got %d", len(all))
	}
	if !strings.Contains(all[0].Content, "dark theme") {
		t.Fatalf("high-confidence fact should rank first, got order: %q then %q", all[0].Content, all[1].Content)
	}
}

func TestLegacyFactDefaultsConfidence(t *testing.T) {
	f := &Fact{Content: "x"}
	if f.confidence() != defaultConfidence {
		t.Fatalf("legacy fact confidence = %v, want default %v", f.confidence(), defaultConfidence)
	}
	f.Confidence = 0.9
	if f.confidence() != 0.9 {
		t.Fatalf("explicit confidence not honored: %v", f.confidence())
	}
}

func TestExactDuplicateReinforces(t *testing.T) {
	fi := newTestFactIndex(t)
	if !fi.AddFactWithMeta("ships nightly via cron job", "pattern", nil, "", ConfidenceExtraction, ProvenanceExtraction) {
		t.Fatal("first add should succeed")
	}
	if fi.AddFactWithMeta("ships nightly via cron job", "pattern", nil, "", ConfidenceExtraction, ProvenanceExtraction) {
		t.Fatal("exact duplicate should not add a second fact")
	}
	f := factByContent(fi.GetAll(), "nightly")
	if f == nil || f.AccessCount != 2 {
		t.Fatalf("duplicate should bump access count to 2, got %+v", f)
	}
	if f.Confidence <= ConfidenceExtraction {
		t.Fatalf("re-observation should raise confidence above %v, got %v", ConfidenceExtraction, f.Confidence)
	}
}

func TestNearDuplicateReinforcesInsteadOfDuplicating(t *testing.T) {
	fi := newTestFactIndex(t)
	fi.AddFactWithMeta("deploys app using kubernetes", "pattern", nil, "", ConfidenceExtraction, ProvenanceExtraction)
	// Same significant tokens, differs only by a stopword → a rephrasing.
	added := fi.AddFactWithMeta("deploys app using the kubernetes", "pattern", nil, "", ConfidenceExtraction, ProvenanceExtraction)
	if added {
		t.Fatal("a rephrasing should reinforce, not add a second fact")
	}
	if n := len(fi.GetAll()); n != 1 {
		t.Fatalf("expected 1 fact after near-duplicate, got %d", n)
	}
}

func TestSupersedeStaleSameSubjectFact(t *testing.T) {
	fi := newTestFactIndex(t)
	fi.AddFactWithMeta("config stored in postgres database", "architecture", nil, "", ConfidenceExtraction, ProvenanceExtraction)
	// Same subject ("config stored"), updated value, at least as confident.
	added := fi.AddFactWithMeta("config stored in mysql database", "architecture", nil, "", ConfidenceUser, ProvenanceUser)
	if !added {
		t.Fatal("an update should be added")
	}
	all := fi.GetAll()
	if len(all) != 1 {
		t.Fatalf("stale fact should have been superseded, got %d facts: %+v", len(all), all)
	}
	got := all[0]
	if !strings.Contains(got.Content, "mysql") {
		t.Fatalf("surviving fact should be the update, got %q", got.Content)
	}
	if !strings.Contains(got.Provenance, "supersedes") {
		t.Fatalf("provenance should record the supersession, got %q", got.Provenance)
	}
}

func TestNoSupersedeWhenSubjectDiffers(t *testing.T) {
	fi := newTestFactIndex(t)
	fi.AddFactWithMeta("config stored in postgres", "architecture", nil, "", ConfidenceUser, ProvenanceUser)
	// Mid-similarity but a DIFFERENT subject ("cache" vs "config") → keep both.
	fi.AddFactWithMeta("cache stored in postgres", "architecture", nil, "", ConfidenceUser, ProvenanceUser)
	if n := len(fi.GetAll()); n != 2 {
		t.Fatalf("different-subject facts must coexist, got %d", n)
	}
}

func TestWeakGuessCannotWipeStrongFact(t *testing.T) {
	fi := newTestFactIndex(t)
	fi.AddFactWithMeta("config stored in postgres database", "architecture", nil, "", ConfidenceUser, ProvenanceUser)
	// Same subject, lower confidence → must NOT supersede the trusted fact.
	fi.AddFactWithMeta("config stored in mysql database", "architecture", nil, "", 0.3, ProvenanceExtraction)
	all := fi.GetAll()
	if len(all) != 2 {
		t.Fatalf("a weak guess must not supersede a strong fact; want 2 facts, got %d", len(all))
	}
	if !strings.Contains(all[0].Content, "postgres") {
		t.Fatalf("the trusted fact should rank first, got %q", all[0].Content)
	}
}
