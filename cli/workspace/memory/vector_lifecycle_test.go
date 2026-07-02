package memory

import (
	"context"
	"testing"

	"go.uber.org/zap"
)

// The vector index is a cache keyed by fact ID. Every fact-removal path must
// drop the matching vector: without that, forgotten/compacted facts leave
// orphan vectors that grow vector_index.json forever, and compaction (which
// rewrites content hashes) forces a full re-embed while the stale vectors
// linger unreferenced.

func TestForgetFactAlsoForgetsItsVector(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, DefaultConfig(), zap.NewNop())
	v := NewVectorIndex(dir, &fakeProvider{dim: 128}, nil)
	m.AttachVectorIndex(v)

	content := "service alpha listens on port 3001 behind nginx"
	if !m.RememberFact(content, "general") {
		t.Fatal("fact not added")
	}
	id := m.Facts.hashContent(content)
	if err := v.BackfillFacts(context.Background(), map[string]string{id: content}); err != nil {
		t.Fatal(err)
	}
	if v.Count() != 1 {
		t.Fatalf("vector not stored: count=%d", v.Count())
	}

	if n := m.ForgetFacts("port 3001"); n != 1 {
		t.Fatalf("ForgetFacts removed %d, want 1", n)
	}
	if v.Count() != 0 {
		t.Fatalf("forgotten fact's vector orphaned: count=%d", v.Count())
	}
}

func TestReplaceFactsForgetsDroppedVectors(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, DefaultConfig(), zap.NewNop())
	v := NewVectorIndex(dir, &fakeProvider{dim: 128}, nil)
	m.AttachVectorIndex(v)

	kept := "service alpha listens on port 3001 behind nginx"
	dropped := "service beta stores queue state in redis db 2"
	m.RememberFact(kept, "general")
	m.RememberFact(dropped, "general")
	keptID := m.Facts.hashContent(kept)
	droppedID := m.Facts.hashContent(dropped)
	if err := v.BackfillFacts(context.Background(), map[string]string{keptID: kept, droppedID: dropped}); err != nil {
		t.Fatal(err)
	}
	if v.Count() != 2 {
		t.Fatalf("vectors not stored: count=%d", v.Count())
	}

	keptFact, ok := m.Facts.GetByID(keptID)
	if !ok {
		t.Fatal("kept fact missing")
	}
	m.Facts.ReplaceFacts([]*Fact{keptFact}) // compaction shape: one dropped

	if got := v.MissingFor([]string{keptID}); len(got) != 0 {
		t.Fatal("kept fact's vector was dropped")
	}
	if v.Count() != 1 {
		t.Fatalf("dropped fact's vector orphaned: count=%d, want 1", v.Count())
	}
}
