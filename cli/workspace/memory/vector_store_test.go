/*
 * ChatCLI - Vector store tests (Phase 3b).
 */
package memory

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/diillson/chatcli/llm/embedding"
)

// fakeProvider returns deterministic vectors so cosine ranking is
// predictable. Embedding(text) = one-hot for the first letter +
// constant tail, ensuring "alpha …" and "alpha foo" land near each
// other while "zebra …" sits far away.
type fakeProvider struct {
	dim int
}

func (f *fakeProvider) Name() string   { return "fake" }
func (f *fakeProvider) Dimension() int { return f.dim }
func (f *fakeProvider) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, f.dim)
		if len(t) > 0 {
			c := t[0]
			if int(c) < f.dim {
				v[c] = 1
			}
		}
		v[0] += 0.1 // shared bias
		out[i] = v
	}
	return out, nil
}

func TestVectorIndex_NilIsSafe(t *testing.T) {
	var v *VectorIndex
	if v.Enabled() {
		t.Error("nil index must report not enabled")
	}
	if v.Count() != 0 {
		t.Error("nil index must count 0")
	}
	if got := v.MissingFor([]string{"x"}); got != nil {
		t.Errorf("nil index MissingFor must be nil; got %v", got)
	}
}

func TestVectorIndex_NullProviderDisablesAll(t *testing.T) {
	dir := t.TempDir()
	v := NewVectorIndex(dir, embedding.NewNull(), nil)
	if v.Enabled() {
		t.Error("null provider must disable index")
	}
	if got := v.SimilarFacts([]float32{1}, 5); got != nil {
		t.Errorf("disabled index Search must be nil; got %v", got)
	}
}

func TestVectorIndex_BackfillAndSearch(t *testing.T) {
	dir := t.TempDir()
	provider := &fakeProvider{dim: 128}
	v := NewVectorIndex(dir, provider, nil)
	if !v.Enabled() {
		t.Fatal("real provider must enable index")
	}

	items := map[string]string{
		"f1": "alpha and gamma",
		"f2": "beta and delta",
		"f3": "alpha story",
	}
	if err := v.BackfillFacts(context.Background(), items); err != nil {
		t.Fatalf("BackfillFacts: %v", err)
	}
	if v.Count() != 3 {
		t.Fatalf("expected 3 entries; got %d", v.Count())
	}

	// Query "alpha" should match f1/f3 close, f2 distant.
	queryVec, err := v.EmbedQuery(context.Background(), "alpha question")
	if err != nil {
		t.Fatalf("EmbedQuery: %v", err)
	}
	hits := v.SimilarFacts(queryVec, 2)
	if len(hits) != 2 {
		t.Fatalf("expected 2 hits; got %v", hits)
	}
	if hits[0] != "f1" && hits[0] != "f3" {
		t.Errorf("top hit should be alpha-prefixed fact; got %v", hits)
	}
}

func TestVectorIndex_PersistAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	provider := &fakeProvider{dim: 16}
	v1 := NewVectorIndex(dir, provider, nil)
	if err := v1.BackfillFacts(context.Background(), map[string]string{"f1": "alpha"}); err != nil {
		t.Fatalf("BackfillFacts: %v", err)
	}

	v2 := NewVectorIndex(dir, provider, nil)
	if v2.Count() != 1 {
		t.Fatalf("persisted index should reload entries; got count=%d, path=%s", v2.Count(), filepath.Join(dir, "vector_index.json"))
	}
}

func TestVectorIndex_MissingForReportsAbsent(t *testing.T) {
	dir := t.TempDir()
	provider := &fakeProvider{dim: 16}
	v := NewVectorIndex(dir, provider, nil)
	_ = v.BackfillFacts(context.Background(), map[string]string{"f1": "alpha"})
	missing := v.MissingFor([]string{"f1", "f2", "f3"})
	if len(missing) != 2 {
		t.Errorf("expected 2 missing; got %v", missing)
	}
}

func TestVectorIndex_ForgetRemoves(t *testing.T) {
	dir := t.TempDir()
	provider := &fakeProvider{dim: 16}
	v := NewVectorIndex(dir, provider, nil)
	_ = v.BackfillFacts(context.Background(), map[string]string{"f1": "alpha", "f2": "beta"})
	v.Forget("f1")
	if v.Count() != 1 {
		t.Errorf("Forget should leave 1 entry; got %d", v.Count())
	}
}
