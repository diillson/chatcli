/*
 * ChatCLI - Long-term memory: fact vector store (adapter over vindex).
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * This is the fact-flavored adapter over the generic llm/embedding/vindex.Index.
 * It owns the "memory" vocabulary — facts, BackfillFacts, SimilarFacts — while
 * the cosine math, bounded top-K selection, JSON persistence and provider/
 * dimension aware auto-migration live exactly once in the shared vindex package.
 * No vector machinery is duplicated per consumer: /context retrieval embeds the
 * very same primitive.
 */
package memory

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/diillson/chatcli/llm/embedding"
	"github.com/diillson/chatcli/llm/embedding/vindex"
	"go.uber.org/zap"
)

// VectorIndex backs HyDE Phase 3b. It is a thin wrapper translating between the
// fact domain and the generic vector index.
type VectorIndex struct {
	idx      *vindex.Index
	provider embedding.Provider
}

// NewVectorIndex constructs the fact vector index persisted under memoryDir. A
// nil/Null provider yields a disabled index whose mutating calls are no-ops and
// whose searches return nothing — callers gate on Enabled().
func NewVectorIndex(memoryDir string, provider embedding.Provider, logger *zap.Logger) *VectorIndex {
	path := filepath.Join(memoryDir, "vector_index.json")
	return &VectorIndex{
		idx:      vindex.New(path, provider, vindex.WithLogger(logger)),
		provider: provider,
	}
}

// Enabled reports whether a real provider backs the index.
func (v *VectorIndex) Enabled() bool {
	return v != nil && v.idx.Enabled()
}

// ProviderName surfaces the configured provider name (or "null") for /config.
func (v *VectorIndex) ProviderName() string {
	if v == nil || v.provider == nil {
		return "null"
	}
	return v.provider.Name()
}

// Count returns the number of vectors currently stored.
func (v *VectorIndex) Count() int {
	if v == nil {
		return 0
	}
	return v.idx.Count()
}

// MissingFor returns the subset of factIDs that have no vector yet.
func (v *VectorIndex) MissingFor(factIDs []string) []string {
	if v == nil {
		return nil
	}
	return v.idx.MissingFor(factIDs)
}

// BackfillFacts embeds and stores the supplied (id,text) fact pairs.
func (v *VectorIndex) BackfillFacts(ctx context.Context, items map[string]string) error {
	if v == nil {
		return nil
	}
	return v.idx.Upsert(ctx, items)
}

// ScoredFact pairs a fact id with the cosine similarity that surfaced it.
type ScoredFact struct {
	ID    string
	Score float64
}

// SimilarFactsScored ranks stored facts by cosine similarity to a query vector,
// returning the top-k that clear minScore, strongest first.
func (v *VectorIndex) SimilarFactsScored(query []float32, k int, minScore float64) []ScoredFact {
	if v == nil {
		return nil
	}
	hits := v.idx.Search(query, k, minScore)
	if len(hits) == 0 {
		return nil
	}
	out := make([]ScoredFact, 0, len(hits))
	for _, h := range hits {
		out = append(out, ScoredFact{ID: h.ID, Score: h.Score})
	}
	return out
}

// vectorScoreEpsilon is the smallest cosine the id-only SimilarFacts admits,
// preserving the historical "strictly positive" semantics without a config knob.
const vectorScoreEpsilon = 1e-9

// SimilarFacts is the id-only convenience wrapper over SimilarFactsScored.
func (v *VectorIndex) SimilarFacts(query []float32, k int) []string {
	scored := v.SimilarFactsScored(query, k, vectorScoreEpsilon)
	if len(scored) == 0 {
		return nil
	}
	out := make([]string, 0, len(scored))
	for _, s := range scored {
		out = append(out, s.ID)
	}
	return out
}

// EmbedQuery embeds a single query string so the retriever can search.
func (v *VectorIndex) EmbedQuery(ctx context.Context, query string) ([]float32, error) {
	if v == nil || v.idx == nil {
		return nil, fmt.Errorf("vector index disabled (no embedding provider)")
	}
	return v.idx.EmbedQuery(ctx, query)
}

// Forget removes facts' vectors. Called by the compactor when a fact is archived
// so we never serve a cosine match against a deleted note.
func (v *VectorIndex) Forget(factIDs ...string) {
	if v == nil {
		return
	}
	v.idx.Forget(factIDs...)
}
