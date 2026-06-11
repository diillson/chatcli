/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Semantic retrieval engine for /context.
 *
 * Raw whole-file injection blows the window on any non-trivial context. The
 * engine answers that: it segments a context into passages, embeds them once
 * (cached on disk per context), and at prompt time returns only the top-k
 * passages relevant to the current question. Provider-agnostic via the shared
 * embedding layer; a Null/absent provider disables retrieval and the manager
 * falls back to the legacy whole-content path with zero regression.
 */
package ctxmgr

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/llm/embedding"
	"github.com/diillson/chatcli/llm/embedding/vindex"
	"go.uber.org/zap"
)

const (
	// DefaultRetrievalTopK is the passage count injected when --rag is used
	// without an explicit number. Exported so the CLI flag parser and the engine
	// share one source of truth.
	DefaultRetrievalTopK = 8

	// segmentScoreFloor keeps near-orthogonal passages out of the result set.
	// Lower than memory's fact floor because passages are smaller and a softer
	// gate trades a little precision for recall on terse code.
	segmentScoreFloor = 0.15
)

// Hybrid fusion weights: semantic similarity leads when available (it already
// paid for the embedding round-trip), lexical BM25 keeps exact-term matches —
// identifiers, env vars, command names — from being washed out.
const (
	hybridVecWeight = 0.55
	hybridLexWeight = 0.45

	// hybridMaxPool caps the BM25 candidate pool, which is also the per-call
	// embedding budget of the rerank stage: the engine NEVER embeds more than
	// this many passages for one query — corpus size does not enter the cost.
	// Comfortably under every provider's batch limit (Voyage caps at 128).
	hybridMaxPool = 96
)

// RetrievalEngine builds and queries per-context passage vectors.
type RetrievalEngine struct {
	provider embedding.Provider
	baseDir  string
	segOpts  SegmentOptions
	logger   *zap.Logger

	// lex memoizes per-context segment lists + BM25 indexes, reused across
	// turns until the context changes (fingerprint mismatch). Held behind a
	// pointer so the engine struct stays comparable — its published API shape
	// — and so copies share one cache instead of tearing a mutex.
	lex *lexCacheStore
}

// lexCacheStore is the shared, locked cache of lexical derivations.
type lexCacheStore struct {
	mu      sync.Mutex
	entries map[string]*lexCacheEntry
}

// lexCacheEntry memoizes the expensive per-context derivations: segments,
// BM25 index and the vector-index handle. Holding the vindex here is what
// keeps the per-call cost flat — constructing it per query re-parses the
// whole persisted JSON, which grows with everything ever embedded.
type lexCacheEntry struct {
	fingerprint string
	segs        []Segment
	lex         *lexicalIndex
	vec         *vindex.Index
}

// NewRetrievalEngine wires an engine over an embedding provider. baseDir is the
// directory where per-context vector caches live (alongside the context JSON).
// A Null provider is a valid input: vector paths report Enabled()=false while
// the lexical (keyless) hybrid path stays fully functional.
func NewRetrievalEngine(provider embedding.Provider, baseDir string, logger *zap.Logger) *RetrievalEngine {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &RetrievalEngine{
		provider: provider,
		baseDir:  baseDir,
		logger:   logger,
		lex:      &lexCacheStore{entries: make(map[string]*lexCacheEntry)},
	}
}

// Enabled reports whether a real embedding provider backs the engine.
func (e *RetrievalEngine) Enabled() bool {
	return e != nil && !embedding.IsNull(e.provider)
}

// Retrieve returns the top-k passages of fc most relevant to query. It builds
// the per-context vector cache lazily, embeds only segments not already cached,
// and evicts vectors for passages that no longer exist (file edited/removed), so
// repeated calls are cheap and never serve a match against stale text.
func (e *RetrievalEngine) Retrieve(ctx context.Context, fc *FileContext, query string, k int) ([]Segment, error) {
	if !e.Enabled() || fc == nil {
		return nil, nil
	}
	if k <= 0 {
		k = DefaultRetrievalTopK
	}
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}

	segs := SegmentFiles(fc.Files, e.segOpts)
	if len(segs) == 0 {
		return nil, nil
	}

	byID := make(map[string]Segment, len(segs))
	keep := make(map[string]struct{}, len(segs))
	allIDs := make([]string, 0, len(segs))
	for _, s := range segs {
		byID[s.ID] = s
		keep[s.ID] = struct{}{}
		allIDs = append(allIDs, s.ID)
	}

	idx := vindex.New(e.vectorPath(fc.ID), e.provider, vindex.WithLogger(e.logger))
	idx.Prune(keep)

	if missing := idx.MissingFor(allIDs); len(missing) > 0 {
		sub := make(map[string]string, len(missing))
		for _, id := range missing {
			sub[id] = byID[id].Content
		}
		if err := idx.Upsert(ctx, sub); err != nil {
			return nil, fmt.Errorf("embed segments: %w", err)
		}
	}

	qv, err := idx.EmbedQuery(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}

	hits := idx.Search(qv, k, segmentScoreFloor)
	out := make([]Segment, 0, len(hits))
	for _, h := range hits {
		if s, ok := byID[h.ID]; ok {
			out = append(out, s)
		}
	}
	return out, nil
}

// RetrieveHybrid returns the top-k passages of fc most relevant to query,
// blending keyless BM25 with cosine similarity when an embedding provider is
// configured. This is the knowledge-mode path: unlike Retrieve it never
// requires an API key — without a provider it degrades to lexical-only, and a
// failing embedding call degrades the same way instead of breaking the turn.
//
// Scalability contract: BM25 does RECALL over the whole corpus (in-memory,
// fingerprint-cached); embeddings only RERANK the candidate pool. Per-query
// embedding cost is bounded by hybridMaxPool regardless of corpus size — a
// 60MB corpus is never embedded wholesale, and the vector cache only ever
// holds passages that some query actually surfaced.
func (e *RetrievalEngine) RetrieveHybrid(ctx context.Context, fc *FileContext, query string, k int) ([]Segment, error) {
	if e == nil || fc == nil || strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if k <= 0 {
		k = DefaultRetrievalTopK
	}
	entry := e.entryFor(fc)
	if len(entry.segs) == 0 {
		return nil, nil
	}
	// Over-fetch so the rerank has real candidates; the pool is also the
	// per-query embedding budget.
	pool := k * 6
	if pool < 48 {
		pool = 48
	}
	if pool > hybridMaxPool {
		pool = hybridMaxPool
	}

	lexHits := entry.lex.search(query, pool)
	if len(lexHits) == 0 {
		// No lexical signal at all (query terms absent from the corpus): fall
		// back to cosine over the vectors already cached by past queries.
		// Cheap — one query embedding, zero new passage embeddings.
		return e.semanticOnly(ctx, entry, query, k), nil
	}
	lexScores := normalizeHits(lexHits)
	vecScores := e.rerankCandidates(ctx, entry, lexHits, query)

	fused := make(map[int]float64, len(lexScores)+len(vecScores))
	if len(vecScores) == 0 {
		fused = lexScores
	} else {
		for i, s := range lexScores {
			fused[i] += hybridLexWeight * s
		}
		for i, s := range vecScores {
			fused[i] += hybridVecWeight * s
		}
	}

	order := make([]int, 0, len(fused))
	for i := range fused {
		order = append(order, i)
	}
	sort.Slice(order, func(a, b int) bool {
		if fused[order[a]] != fused[order[b]] {
			return fused[order[a]] > fused[order[b]]
		}
		return order[a] < order[b]
	})
	if len(order) > k {
		order = order[:k]
	}
	out := make([]Segment, 0, len(order))
	for _, i := range order {
		out = append(out, entry.segs[i])
	}
	return out, nil
}

// rerankCandidates embeds ONLY the BM25 candidates still missing from the
// vector cache (≤ pool per query, one small provider batch), then returns
// min-max-normalized cosine scores by segment index. Nil when embeddings are
// unavailable or fail — the hybrid then runs lexical-only.
func (e *RetrievalEngine) rerankCandidates(ctx context.Context, entry *lexCacheEntry, candidates []lexHit, query string) map[int]float64 {
	if !e.Enabled() || entry.vec == nil {
		return nil
	}
	idxByID := make(map[string]int, len(candidates))
	ids := make([]string, 0, len(candidates))
	for _, h := range candidates {
		s := entry.segs[h.idx]
		idxByID[s.ID] = h.idx
		ids = append(ids, s.ID)
	}
	if missing := entry.vec.MissingFor(ids); len(missing) > 0 {
		sub := make(map[string]string, len(missing))
		for _, id := range missing {
			sub[id] = entry.segs[idxByID[id]].Content
		}
		if err := entry.vec.Upsert(ctx, sub); err != nil {
			e.logger.Warn("knowledge: embedding unavailable; falling back to lexical retrieval", zap.Error(err))
			return nil
		}
	}
	qv, err := entry.vec.EmbedQuery(ctx, query)
	if err != nil {
		e.logger.Warn("knowledge: query embedding failed; falling back to lexical retrieval", zap.Error(err))
		return nil
	}
	hits := entry.vec.ScoreAgainst(qv, ids, segmentScoreFloor)
	if len(hits) == 0 {
		return nil
	}
	minS, maxS := hits[len(hits)-1].Score, hits[0].Score
	out := make(map[int]float64, len(hits))
	for _, h := range hits {
		i, ok := idxByID[h.ID]
		if !ok {
			continue
		}
		if maxS > minS {
			out[i] = (h.Score - minS) / (maxS - minS)
		} else {
			out[i] = 1
		}
	}
	return out
}

// semanticOnly answers queries with zero lexical overlap using the vectors
// already accumulated by previous queries. It never embeds new passages.
func (e *RetrievalEngine) semanticOnly(ctx context.Context, entry *lexCacheEntry, query string, k int) []Segment {
	if !e.Enabled() || entry.vec == nil || entry.vec.Count() == 0 {
		return nil
	}
	qv, err := entry.vec.EmbedQuery(ctx, query)
	if err != nil {
		e.logger.Warn("knowledge: query embedding failed; no results for non-lexical query", zap.Error(err))
		return nil
	}
	byID := make(map[string]Segment, len(entry.segs))
	for _, s := range entry.segs {
		byID[s.ID] = s
	}
	hits := entry.vec.Search(qv, k, segmentScoreFloor)
	out := make([]Segment, 0, len(hits))
	for _, h := range hits {
		if s, ok := byID[h.ID]; ok {
			out = append(out, s)
		}
	}
	return out
}

// entryFor returns the cached derivations for fc — segments, BM25 index and
// the vector-index handle — rebuilding them when the context changed. The
// vindex file is loaded ONCE per rebuild (not per query) and pruned of
// segments that no longer exist at the same moment.
func (e *RetrievalEngine) entryFor(fc *FileContext) *lexCacheEntry {
	fp := contextFingerprint(fc)
	if e.lex == nil {
		// Engine built without the constructor (zero value): compute uncached.
		return e.buildEntry(fc, fp)
	}
	e.lex.mu.Lock()
	defer e.lex.mu.Unlock()
	if entry, ok := e.lex.entries[fc.ID]; ok && entry.fingerprint == fp {
		return entry
	}
	entry := e.buildEntry(fc, fp)
	e.lex.entries[fc.ID] = entry
	return entry
}

// buildEntry performs the one-time-per-corpus derivations and logs their cost
// so a multi-second first call on a large corpus is explainable, not mysterious.
func (e *RetrievalEngine) buildEntry(fc *FileContext, fp string) *lexCacheEntry {
	start := time.Now()
	segs := SegmentFiles(fc.Files, e.segOpts)
	entry := &lexCacheEntry{fingerprint: fp, segs: segs, lex: newLexicalIndex(segs)}
	if e.Enabled() {
		entry.vec = vindex.New(e.vectorPath(fc.ID), e.provider, vindex.WithLogger(e.logger))
		keep := make(map[string]struct{}, len(segs))
		for _, s := range segs {
			keep[s.ID] = struct{}{}
		}
		entry.vec.Prune(keep)
	}
	e.logger.Info("knowledge: corpus indexed",
		zap.String("context", fc.Name),
		zap.Int("passages", len(segs)),
		zap.Duration("took", time.Since(start)))
	return entry
}

// contextFingerprint identifies one revision of a context's content; caches
// keyed by it invalidate exactly when the context is updated.
func contextFingerprint(fc *FileContext) string {
	return fmt.Sprintf("%s|%d|%d", fc.UpdatedAt.UTC().Format("20060102T150405.000"), fc.FileCount, fc.TotalSize)
}

// normalizeHits min-max-normalizes BM25 scores to [0,1] by segment index.
func normalizeHits(hits []lexHit) map[int]float64 {
	if len(hits) == 0 {
		return nil
	}
	minS, maxS := hits[len(hits)-1].score, hits[0].score
	out := make(map[int]float64, len(hits))
	for _, h := range hits {
		if maxS > minS {
			out[h.idx] = (h.score - minS) / (maxS - minS)
		} else {
			out[h.idx] = 1
		}
	}
	return out
}

// DropCache removes a context's persisted vector file. Called when a context is
// deleted or its files change wholesale, so no orphaned cache lingers on disk.
func (e *RetrievalEngine) DropCache(contextID string) {
	if e == nil {
		return
	}
	if e.lex != nil {
		e.lex.mu.Lock()
		delete(e.lex.entries, contextID)
		e.lex.mu.Unlock()
	}
	idx := vindex.New(e.vectorPath(contextID), e.provider, vindex.WithLogger(e.logger))
	idx.Prune(map[string]struct{}{}) // empty keep-set → drops all + removes file
}

func (e *RetrievalEngine) vectorPath(contextID string) string {
	return filepath.Join(e.baseDir, contextID+".vec.json")
}

// FormatSegmentsBlock renders retrieved passages as a prompt block. The format
// mirrors formatChunk (literal English/emoji, not i18n — this is model-facing
// content, and the codebase keeps prompt scaffolding in English on purpose) and
// annotates each passage with its source file and line range so the model can
// cite precisely and the user can trace what was injected.
func FormatSegmentsBlock(contextName, query string, segs []Segment) string {
	if len(segs) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "🔎 CONTEXT (semantic retrieval): %s — %d relevant passage(s)\n", contextName, len(segs))
	b.WriteString("Only the passages most relevant to the current request are shown; ")
	b.WriteString("ask for more or attach without --rag to see the full context.\n\n")
	writeSegments(&b, segs)
	return b.String()
}

// FormatKnowledgeSegmentsBlock renders passages pulled from a knowledge base.
// Same model-facing English scaffolding as FormatSegmentsBlock, with wording
// that matches the index-card contract: the corpus is searchable, not attached.
func FormatKnowledgeSegmentsBlock(contextName string, segs []Segment) string {
	if len(segs) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "📚 KNOWLEDGE (retrieved): %s — %d relevant passage(s)\n", contextName, len(segs))
	b.WriteString("Auto-retrieved from the knowledge base for the current request; ")
	b.WriteString("the full corpus stays out of context and is searched per turn.\n\n")
	writeSegments(&b, segs)
	return b.String()
}

// writeSegments renders the shared passage list: source annotation, position
// and fenced content, so the model can cite and the user can trace injections.
func writeSegments(b *strings.Builder, segs []Segment) {
	for i, s := range segs {
		fmt.Fprintf(b, "📄 %s (lines %d-%d) [%d/%d]\n", s.FilePath, s.StartLine, s.EndLine, i+1, len(segs))
		b.WriteString("```\n")
		b.WriteString(s.Content)
		b.WriteString("\n```\n\n")
	}
}
