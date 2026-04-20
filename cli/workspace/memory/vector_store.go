/*
 * ChatCLI - Long-term memory: vector store for HyDE Phase 3b.
 *
 * Pure-Go cosine similarity over a JSON-persisted map of fact_id →
 * embedding. Deliberately not SQLite/sqlite-vec to keep CGO out of
 * the build matrix; for the typical chatcli memory size (hundreds of
 * facts) a linear scan on float32 vectors is microsecond-cheap.
 *
 * The store is dimension-locked: switching providers (e.g. voyage 1024
 * → openai 1536) is rejected with an error so cosine math stays sound.
 * Operators clear the file (~/.chatcli/memory/vector_index.json) when
 * they want to migrate.
 */
package memory

import (
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

// VectorEntry pairs a fact id with its persisted embedding. Stored as
// the disk shape; in-memory the index keeps a parallel float32 slice
// to avoid per-call allocation.
type VectorEntry struct {
	FactID    string    `json:"fact_id"`
	Vector    []float32 `json:"vector"`
	Dimension int       `json:"dim"`
	Provider  string    `json:"provider"`
}

// VectorIndex is the cosine-similarity store backing HyDE vector search.
type VectorIndex struct {
	mu        sync.RWMutex
	provider  embedding.Provider
	dimension int
	entries   map[string]*VectorEntry
	path      string
	logger    *zap.Logger
}

// NewVectorIndex constructs the index. provider may be nil/Null —
// the index then becomes a no-op (Search returns nothing, Upsert
// silently skips). When provider is real, the on-disk file is loaded
// if present and dimension-checked against provider.Dimension.
func NewVectorIndex(memoryDir string, provider embedding.Provider, logger *zap.Logger) *VectorIndex {
	if logger == nil {
		logger = zap.NewNop()
	}
	idx := &VectorIndex{
		provider: provider,
		entries:  make(map[string]*VectorEntry),
		path:     filepath.Join(memoryDir, "vector_index.json"),
		logger:   logger,
	}
	if !embedding.IsNull(provider) {
		idx.dimension = provider.Dimension()
		idx.load()
	}
	return idx
}

// Enabled reports whether the index has a real provider behind it.
// Callers use this to short-circuit cheap paths without reflection.
func (v *VectorIndex) Enabled() bool {
	if v == nil {
		return false
	}
	return !embedding.IsNull(v.provider)
}

// ProviderName surfaces the configured provider name (or "null") for
// /config quality. Read-only — does not mutate the index.
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
	v.mu.RLock()
	defer v.mu.RUnlock()
	return len(v.entries)
}

// MissingFor returns the subset of factIDs that have no vector
// stored yet. The HyDE retriever feeds this to BackfillFacts so
// embeddings are computed lazily as facts surface.
func (v *VectorIndex) MissingFor(factIDs []string) []string {
	if v == nil || !v.Enabled() {
		return nil
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	out := make([]string, 0)
	for _, id := range factIDs {
		if _, ok := v.entries[id]; !ok {
			out = append(out, id)
		}
	}
	return out
}

// BackfillFacts computes embeddings for the supplied facts (id,text)
// pairs and stores them. Errors from the provider are returned but
// any successfully-embedded entries are still persisted, so a partial
// failure leaves the store usable.
func (v *VectorIndex) BackfillFacts(ctx context.Context, items map[string]string) error {
	if v == nil || !v.Enabled() || len(items) == 0 {
		return nil
	}
	ids := make([]string, 0, len(items))
	texts := make([]string, 0, len(items))
	for id, text := range items {
		ids = append(ids, id)
		texts = append(texts, text)
	}
	vecs, err := v.provider.Embed(ctx, texts)
	if err != nil {
		return fmt.Errorf("embed batch: %w", err)
	}
	if len(vecs) != len(ids) {
		return fmt.Errorf("provider returned %d vectors for %d inputs", len(vecs), len(ids))
	}
	v.mu.Lock()
	for i, vec := range vecs {
		if len(vec) == 0 {
			continue
		}
		if v.dimension == 0 {
			v.dimension = len(vec)
		}
		if len(vec) != v.dimension {
			v.mu.Unlock()
			return fmt.Errorf("provider %s emitted dim=%d but store dim=%d (clear %s to migrate)",
				v.provider.Name(), len(vec), v.dimension, v.path)
		}
		v.entries[ids[i]] = &VectorEntry{
			FactID:    ids[i],
			Vector:    vec,
			Dimension: v.dimension,
			Provider:  v.provider.Name(),
		}
	}
	v.mu.Unlock()
	return v.persist()
}

// SimilarFacts ranks stored entries by cosine similarity to a query
// vector, returning the top-k fact ids. k <= 0 returns nothing.
func (v *VectorIndex) SimilarFacts(query []float32, k int) []string {
	if v == nil || !v.Enabled() || k <= 0 || len(query) == 0 {
		return nil
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	if len(v.entries) == 0 {
		return nil
	}
	type scored struct {
		id    string
		score float32
	}
	hits := make([]scored, 0, len(v.entries))
	for id, e := range v.entries {
		if len(e.Vector) != len(query) {
			continue
		}
		hits = append(hits, scored{id: id, score: embedding.CosineSimilarity(query, e.Vector)})
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	if k > len(hits) {
		k = len(hits)
	}
	out := make([]string, 0, k)
	for i := 0; i < k; i++ {
		if hits[i].score <= 0 {
			break
		}
		out = append(out, hits[i].id)
	}
	return out
}

// EmbedQuery delegates to the provider so the retriever can ask for
// the query vector once and pass it to SimilarFacts.
func (v *VectorIndex) EmbedQuery(ctx context.Context, query string) ([]float32, error) {
	if v == nil || !v.Enabled() {
		return nil, fmt.Errorf("vector index disabled (no embedding provider)")
	}
	vecs, err := v.provider.Embed(ctx, []string{query})
	if err != nil {
		return nil, err
	}
	if len(vecs) == 0 {
		return nil, fmt.Errorf("provider returned 0 vectors for query")
	}
	return vecs[0], nil
}

// Forget removes a fact's vector. Called by the compactor when a fact
// is archived so we never serve cosine matches against a deleted note.
func (v *VectorIndex) Forget(factIDs ...string) {
	if v == nil || !v.Enabled() || len(factIDs) == 0 {
		return
	}
	v.mu.Lock()
	for _, id := range factIDs {
		delete(v.entries, id)
	}
	v.mu.Unlock()
	if err := v.persist(); err != nil {
		v.logger.Warn("vector_index persist failed after Forget", zap.Error(err))
	}
}

// ─── Persistence ──────────────────────────────────────────────────────────

type vectorIndexFile struct {
	Dimension int                     `json:"dimension"`
	Provider  string                  `json:"provider"`
	Entries   map[string]*VectorEntry `json:"entries"`
}

func (v *VectorIndex) load() {
	data, err := os.ReadFile(v.path)
	if err != nil {
		if !os.IsNotExist(err) {
			v.logger.Warn("vector_index read failed", zap.String("path", v.path), zap.Error(err))
		}
		return
	}
	var f vectorIndexFile
	if err := json.Unmarshal(data, &f); err != nil {
		v.logger.Warn("vector_index unmarshal failed", zap.Error(err))
		return
	}
	if v.dimension > 0 && f.Dimension > 0 && f.Dimension != v.dimension {
		v.logger.Warn("vector_index dimension mismatch — discarding cache",
			zap.Int("on_disk_dim", f.Dimension),
			zap.Int("provider_dim", v.dimension),
			zap.String("provider", v.provider.Name()))
		return
	}
	if f.Dimension > 0 {
		v.dimension = f.Dimension
	}
	if f.Entries != nil {
		v.entries = f.Entries
	}
}

func (v *VectorIndex) persist() error {
	v.mu.RLock()
	f := vectorIndexFile{
		Dimension: v.dimension,
		Provider:  v.provider.Name(),
		Entries:   v.entries,
	}
	v.mu.RUnlock()
	if err := os.MkdirAll(filepath.Dir(v.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(v.path, data, 0o644)
}
