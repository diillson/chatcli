/*
 * ChatCLI - Generic cosine vector index.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * A provider- and domain-agnostic cosine-similarity index over a JSON-persisted
 * map of id → embedding. It is the reusable primitive extracted once a second
 * consumer (semantic /context retrieval) appeared alongside long-term memory:
 * the same proven mechanics — bounded top-K selection, a relevance floor, and
 * provider/dimension aware auto-migration — with no coupling to what the ids
 * mean (facts, document segments, anything keyable by string).
 *
 * Pure Go, no CGO, no vector DB: a linear scan over float32 with an O(n log k)
 * heap is microsecond-cheap for the thousands-of-segments range this serves,
 * and it keeps the build matrix clean across Windows, Linux and macOS.
 */
package vindex

import (
	"container/heap"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/diillson/chatcli/llm/embedding"
	"go.uber.org/zap"
)

// Hit pairs an id with the cosine similarity that surfaced it.
type Hit struct {
	ID    string
	Score float64
}

// Index is a cosine-similarity store keyed by arbitrary string ids.
type Index struct {
	mu       sync.RWMutex
	provider embedding.Provider
	dim      int
	entries  map[string][]float32
	path     string
	logger   *zap.Logger
}

// Option customizes an Index at construction.
type Option func(*Index)

// WithLogger attaches a logger (defaults to a no-op).
func WithLogger(l *zap.Logger) Option {
	return func(x *Index) {
		if l != nil {
			x.logger = l
		}
	}
}

// New constructs an index persisted at path, backed by provider. A nil/Null
// provider yields a disabled index whose mutating calls are no-ops and whose
// Search returns nothing — callers gate cheap paths on Enabled().
func New(path string, provider embedding.Provider, opts ...Option) *Index {
	x := &Index{
		provider: provider,
		entries:  make(map[string][]float32),
		path:     path,
		logger:   zap.NewNop(),
	}
	for _, opt := range opts {
		opt(x)
	}
	if !embedding.IsNull(provider) {
		x.dim = provider.Dimension()
		x.load()
	}
	return x
}

// Enabled reports whether a real provider backs the index.
func (x *Index) Enabled() bool {
	return x != nil && !embedding.IsNull(x.provider)
}

// Count returns the number of stored vectors.
func (x *Index) Count() int {
	if x == nil {
		return 0
	}
	x.mu.RLock()
	defer x.mu.RUnlock()
	return len(x.entries)
}

// Has reports whether id already has a stored vector.
func (x *Index) Has(id string) bool {
	if x == nil {
		return false
	}
	x.mu.RLock()
	defer x.mu.RUnlock()
	_, ok := x.entries[id]
	return ok
}

// MissingFor returns the subset of ids lacking a stored vector, preserving
// input order, so callers can embed only what changed.
func (x *Index) MissingFor(ids []string) []string {
	if !x.Enabled() {
		return nil
	}
	x.mu.RLock()
	defer x.mu.RUnlock()
	out := make([]string, 0)
	for _, id := range ids {
		if _, ok := x.entries[id]; !ok {
			out = append(out, id)
		}
	}
	return out
}

// Upsert embeds the supplied id→text pairs in one provider batch and stores the
// vectors. Partial success is preserved: any vectors obtained before an error
// are still written, so a flaky provider degrades rather than losing progress.
func (x *Index) Upsert(ctx context.Context, items map[string]string) error {
	if !x.Enabled() || len(items) == 0 {
		return nil
	}
	ids := make([]string, 0, len(items))
	texts := make([]string, 0, len(items))
	for id, text := range items {
		ids = append(ids, id)
		texts = append(texts, text)
	}
	vecs, err := x.provider.Embed(ctx, texts)
	if err != nil {
		return fmt.Errorf("embed batch: %w", err)
	}
	if len(vecs) != len(ids) {
		return fmt.Errorf("provider returned %d vectors for %d inputs", len(vecs), len(ids))
	}
	x.mu.Lock()
	for i, vec := range vecs {
		if len(vec) == 0 {
			continue
		}
		if x.dim == 0 {
			x.dim = len(vec)
		}
		if len(vec) != x.dim {
			x.mu.Unlock()
			return fmt.Errorf("provider %s emitted dim=%d but index dim=%d", x.provider.Name(), len(vec), x.dim)
		}
		x.entries[ids[i]] = vec
	}
	x.mu.Unlock()
	return x.persist()
}

// EmbedQuery embeds a single query string for use with Search.
func (x *Index) EmbedQuery(ctx context.Context, query string) ([]float32, error) {
	if !x.Enabled() {
		return nil, fmt.Errorf("vector index disabled (no embedding provider)")
	}
	vecs, err := x.provider.Embed(ctx, []string{query})
	if err != nil {
		return nil, err
	}
	if len(vecs) == 0 {
		return nil, fmt.Errorf("provider returned 0 vectors for query")
	}
	return vecs[0], nil
}

// Search returns the top-k ids by cosine similarity to query that clear
// minScore, strongest first. minScore is a per-call relevance floor: embeddings
// over normalized text are almost always weakly positive, so a floor (≈0.15–0.30)
// keeps near-orthogonal noise out. Selection is a bounded min-heap so cost scales
// with k, not corpus size; ties break by ascending id for output that is
// deterministic across platforms (map order never leaks through).
func (x *Index) Search(query []float32, k int, minScore float64) []Hit {
	if !x.Enabled() || k <= 0 || len(query) == 0 {
		return nil
	}
	x.mu.RLock()
	defer x.mu.RUnlock()
	if len(x.entries) == 0 {
		return nil
	}
	sel := newTopK(k)
	for id, vec := range x.entries {
		if len(vec) != len(query) {
			continue
		}
		score := float64(embedding.CosineSimilarity(query, vec))
		if score < minScore {
			continue
		}
		sel.offer(id, score)
	}
	return sel.sortedDesc()
}

// ScoreAgainst scores ONLY the given ids against query, returning hits that
// clear minScore, strongest first (ties by ascending id). This is the rerank
// primitive: callers that pre-filter candidates (e.g. BM25 recall) pay cosine
// cost proportional to the candidate pool, never the index size. Ids without
// a stored vector are skipped silently.
func (x *Index) ScoreAgainst(query []float32, ids []string, minScore float64) []Hit {
	if !x.Enabled() || len(query) == 0 || len(ids) == 0 {
		return nil
	}
	x.mu.RLock()
	defer x.mu.RUnlock()
	out := make([]Hit, 0, len(ids))
	for _, id := range ids {
		vec, ok := x.entries[id]
		if !ok || len(vec) != len(query) {
			continue
		}
		score := float64(embedding.CosineSimilarity(query, vec))
		if score < minScore {
			continue
		}
		out = append(out, Hit{ID: id, Score: score})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Forget removes the given ids and persists.
func (x *Index) Forget(ids ...string) {
	if !x.Enabled() || len(ids) == 0 {
		return
	}
	x.mu.Lock()
	changed := false
	for _, id := range ids {
		if _, ok := x.entries[id]; ok {
			delete(x.entries, id)
			changed = true
		}
	}
	x.mu.Unlock()
	if changed {
		if err := x.persist(); err != nil {
			x.logger.Warn("vindex persist failed after Forget", zap.Error(err))
		}
	}
}

// Prune drops every entry whose id is absent from keep, then persists if
// anything changed. Callers use it to evict vectors for content that no longer
// exists (e.g. segments of a file that was edited), so the index never serves a
// match against deleted text.
func (x *Index) Prune(keep map[string]struct{}) {
	if !x.Enabled() {
		return
	}
	x.mu.Lock()
	changed := false
	for id := range x.entries {
		if _, ok := keep[id]; !ok {
			delete(x.entries, id)
			changed = true
		}
	}
	x.mu.Unlock()
	if changed {
		if err := x.persist(); err != nil {
			x.logger.Warn("vindex persist failed after Prune", zap.Error(err))
		}
	}
}

// ─── Persistence ──────────────────────────────────────────────────────────

type indexFile struct {
	Dimension int                  `json:"dimension"`
	Provider  string               `json:"provider"`
	Entries   map[string][]float32 `json:"entries"`
}

func (x *Index) load() {
	data, err := os.ReadFile(x.path)
	if err != nil {
		if !os.IsNotExist(err) {
			x.logger.Warn("vindex read failed", zap.String("path", x.path), zap.Error(err))
		}
		return
	}
	var f indexFile
	if err := json.Unmarshal(data, &f); err != nil {
		x.logger.Warn("vindex unmarshal failed", zap.Error(err))
		return
	}
	// Dimension mismatch: cosine across different arities is undefined.
	if x.dim > 0 && f.Dimension > 0 && f.Dimension != x.dim {
		x.logger.Warn("vindex dimension mismatch — auto-clearing for re-embed",
			zap.Int("on_disk_dim", f.Dimension), zap.Int("provider_dim", x.dim), zap.String("path", x.path))
		x.discardStale()
		return
	}
	// Provider mismatch at the same dimension: two embedding spaces are not
	// comparable, so reusing the cache would serve meaningless matches.
	if len(f.Entries) > 0 && f.Provider != x.provider.Name() {
		x.logger.Warn("vindex provider changed — auto-clearing for re-embed",
			zap.String("on_disk_provider", f.Provider), zap.String("provider", x.provider.Name()), zap.String("path", x.path))
		x.discardStale()
		return
	}
	if f.Dimension > 0 {
		x.dim = f.Dimension
	}
	if f.Entries != nil {
		x.entries = f.Entries
	}
}

// discardStale resets the in-memory cache and removes the on-disk file so the
// next Upsert repopulates a clean, provider-consistent index — no manual rm.
func (x *Index) discardStale() {
	x.entries = make(map[string][]float32)
	x.dim = x.provider.Dimension()
	if err := os.Remove(x.path); err != nil && !os.IsNotExist(err) {
		x.logger.Warn("vindex stale file removal failed (overwritten on next persist)",
			zap.String("path", x.path), zap.Error(err))
	}
}

func (x *Index) persist() error {
	x.mu.RLock()
	f := indexFile{Dimension: x.dim, Provider: x.provider.Name(), Entries: x.entries}
	x.mu.RUnlock()
	if err := os.MkdirAll(filepath.Dir(x.path), 0o750); err != nil {
		return err
	}
	// Compact encoding on purpose: a corpus-scale index serializes to tens of
	// MB, and indented JSON made every persist (and the file itself) several
	// times heavier for zero benefit — nobody reads vectors by eye.
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return os.WriteFile(x.path, data, 0o600)
}

// ─── bounded top-K (min-heap) ─────────────────────────────────────────────

type heapItem struct {
	id    string
	score float64
}

type minHeap []heapItem

func (h minHeap) Len() int           { return len(h) }
func (h minHeap) Less(i, j int) bool { return h[i].score < h[j].score }
func (h minHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *minHeap) Push(x any)        { *h = append(*h, x.(heapItem)) }
func (h *minHeap) Pop() any {
	old := *h
	n := len(old)
	it := old[n-1]
	*h = old[:n-1]
	return it
}

type topK struct {
	k int
	h minHeap
}

func newTopK(k int) *topK {
	if k < 1 {
		k = 1
	}
	return &topK{k: k, h: make(minHeap, 0, k+1)}
}

func (t *topK) offer(id string, score float64) {
	if t.h.Len() < t.k {
		heap.Push(&t.h, heapItem{id: id, score: score})
		return
	}
	if t.h[0].score >= score {
		return
	}
	t.h[0] = heapItem{id: id, score: score}
	heap.Fix(&t.h, 0)
}

func (t *topK) sortedDesc() []Hit {
	out := make([]Hit, t.h.Len())
	for i, it := range t.h {
		out[i] = Hit{ID: it.id, Score: it.score}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].ID < out[j].ID
	})
	return out
}
