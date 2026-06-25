/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package memory

import (
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
)

func TestLegacyConfidenceMapping(t *testing.T) {
	if c := legacyConfidence(0); c != 0.5 {
		t.Errorf("access 0 -> %v, want 0.5 floor", c)
	}
	low, high := legacyConfidence(1), legacyConfidence(50)
	if !(high > low) {
		t.Errorf("more re-observations should raise confidence: %v vs %v", low, high)
	}
	if high > 0.85 {
		t.Errorf("legacy confidence must cap at 0.85, got %v", high)
	}
}

func TestBackfillIsIdempotentAndNonDestructive(t *testing.T) {
	fi := newTestFactIndex(t)
	// A legacy fact (no confidence) and an already-enriched one.
	fi.facts["legacy"] = &Fact{ID: "legacy", Content: "old fact", AccessCount: 4}
	fi.facts["fresh"] = &Fact{ID: "fresh", Content: "new fact", AccessCount: 1, Confidence: 0.9, Provenance: ProvenanceUser}

	if !fi.backfillLegacyConfidenceLocked() {
		t.Fatal("expected the legacy fact to be enriched")
	}
	if fi.facts["legacy"].Confidence <= 0 || fi.facts["legacy"].Provenance != ProvenanceLegacy {
		t.Fatalf("legacy fact not enriched: %+v", fi.facts["legacy"])
	}
	// The already-enriched fact must be untouched.
	if fi.facts["fresh"].Confidence != 0.9 || fi.facts["fresh"].Provenance != ProvenanceUser {
		t.Fatalf("enriched fact was modified: %+v", fi.facts["fresh"])
	}
	// Idempotent: a second pass changes nothing.
	if fi.backfillLegacyConfidenceLocked() {
		t.Fatal("backfill should be idempotent")
	}
	// Non-destructive: no facts removed.
	if len(fi.facts) != 2 {
		t.Fatalf("backfill removed facts: %d remain", len(fi.facts))
	}
}

func TestBackfillRunsOnLoadAndPersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "memory_index.json")
	// A pre-confidence index file (no "confidence"/"provenance" fields).
	legacy := `[{"id":"a","content":"uses postgres","category":"architecture","access_count":3,"score":1.0}]`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	// Loading enriches and rewrites the file.
	fi := NewFactIndex(dir, DefaultConfig(), zap.NewNop())
	f, ok := fi.GetByID("a")
	if !ok || f.Confidence <= 0 || f.Provenance != ProvenanceLegacy {
		t.Fatalf("load did not enrich legacy fact: %+v", f)
	}

	// The rewrite persisted confidence: a fresh load sees it already set.
	got := f.Confidence
	fi2 := NewFactIndex(dir, DefaultConfig(), zap.NewNop())
	if f2, ok := fi2.GetByID("a"); !ok || f2.Confidence != got {
		t.Fatalf("confidence was not persisted across loads: %+v", f2)
	}
}
