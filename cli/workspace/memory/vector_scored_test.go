package memory

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// namedProvider is a second deterministic provider whose Name() and Dimension()
// are configurable, so tests can simulate a provider/dimension SWITCH against an
// already-persisted index. Embedding is one-hot on the first byte (same shape as
// fakeProvider) — enough for the auto-migration paths, which key off the
// persisted provider name and dimension, not the vector values.
type namedProvider struct {
	name string
	dim  int
}

func (p *namedProvider) Name() string   { return p.name }
func (p *namedProvider) Dimension() int { return p.dim }
func (p *namedProvider) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, p.dim)
		if len(t) > 0 && int(t[0]) < p.dim {
			v[t[0]] = 1
		}
		out[i] = v
	}
	return out, nil
}

func TestSimilarFactsScored_FloorFiltersWeakHits(t *testing.T) {
	dir := t.TempDir()
	v := NewVectorIndex(dir, &fakeProvider{dim: 128}, nil)
	if err := v.BackfillFacts(context.Background(), map[string]string{
		"f1": "alpha", // near the query
		"f2": "beta",  // far from the query
	}); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	q, err := v.EmbedQuery(context.Background(), "alpha question")
	if err != nil {
		t.Fatalf("embed query: %v", err)
	}

	// No floor: both candidates returned (f2 weakly positive).
	if got := v.SimilarFactsScored(q, 5, 0.0); len(got) != 2 {
		t.Fatalf("floor 0.0 should return both, got %d: %+v", len(got), got)
	}
	// Real floor: the weak/near-orthogonal f2 is rejected.
	got := v.SimilarFactsScored(q, 5, 0.5)
	if len(got) != 1 || got[0].ID != "f1" {
		t.Fatalf("floor 0.5 should keep only f1, got %+v", got)
	}
	if got[0].Score < 0.99 {
		t.Fatalf("f1 score should be ~1.0, got %.4f", got[0].Score)
	}
}

func TestVectorIndex_ProviderChangeAutoMigrates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vector_index.json")

	// Persist with provider "fake".
	v1 := NewVectorIndex(dir, &fakeProvider{dim: 16}, nil)
	if err := v1.BackfillFacts(context.Background(), map[string]string{"f1": "alpha"}); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("index file should exist after backfill: %v", err)
	}

	// Reopen with a DIFFERENT provider at the same dimension. Cosine across two
	// embedding spaces is meaningless, so the cache must auto-clear rather than
	// silently serving garbage matches.
	v2 := NewVectorIndex(dir, &namedProvider{name: "other", dim: 16}, nil)
	if v2.Count() != 0 {
		t.Fatalf("provider switch must clear the stale cache, got count=%d", v2.Count())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("stale index file should have been removed, stat err=%v", err)
	}
}

func TestVectorIndex_DimensionChangeAutoMigrates(t *testing.T) {
	dir := t.TempDir()

	v1 := NewVectorIndex(dir, &fakeProvider{dim: 16}, nil)
	if err := v1.BackfillFacts(context.Background(), map[string]string{"f1": "alpha"}); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	// Same provider semantics but a different dimension → must clear.
	v2 := NewVectorIndex(dir, &fakeProvider{dim: 32}, nil)
	if v2.Count() != 0 {
		t.Fatalf("dimension switch must clear the stale cache, got count=%d", v2.Count())
	}
}

func TestVectorIndex_SameProviderReloads(t *testing.T) {
	dir := t.TempDir()
	v1 := NewVectorIndex(dir, &fakeProvider{dim: 16}, nil)
	if err := v1.BackfillFacts(context.Background(), map[string]string{"f1": "alpha", "f2": "beta"}); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	// Reopen with the SAME provider+dim → entries must survive (no false migrate).
	v2 := NewVectorIndex(dir, &fakeProvider{dim: 16}, nil)
	if v2.Count() != 2 {
		t.Fatalf("same provider should reload entries, got count=%d", v2.Count())
	}
}
