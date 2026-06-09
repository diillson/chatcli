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
	"strings"

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

// RetrievalEngine builds and queries per-context passage vectors.
type RetrievalEngine struct {
	provider embedding.Provider
	baseDir  string
	segOpts  SegmentOptions
	logger   *zap.Logger
}

// NewRetrievalEngine wires an engine over an embedding provider. baseDir is the
// directory where per-context vector caches live (alongside the context JSON).
func NewRetrievalEngine(provider embedding.Provider, baseDir string, logger *zap.Logger) *RetrievalEngine {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &RetrievalEngine{
		provider: provider,
		baseDir:  baseDir,
		logger:   logger,
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

// DropCache removes a context's persisted vector file. Called when a context is
// deleted or its files change wholesale, so no orphaned cache lingers on disk.
func (e *RetrievalEngine) DropCache(contextID string) {
	if e == nil {
		return
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
	for i, s := range segs {
		fmt.Fprintf(&b, "📄 %s (lines %d-%d) [%d/%d]\n", s.FilePath, s.StartLine, s.EndLine, i+1, len(segs))
		b.WriteString("```\n")
		b.WriteString(s.Content)
		b.WriteString("\n```\n\n")
	}
	return b.String()
}
