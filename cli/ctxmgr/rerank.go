/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Optional rerank stage for knowledge retrieval.
 *
 * Hybrid retrieval (BM25 recall + embedding rescore) ranks passages by a
 * fused score and truncates to k. A Reranker sits between fusion and
 * truncation and reorders the head of the pool with a stronger signal:
 *
 *   - MMRReranker (keyless): maximal marginal relevance over token
 *     overlap, so the k passages returned cover different parts of the
 *     corpus instead of five near-duplicates of the best hit;
 *   - LLMReranker: listwise judgement by a (cheap) model — the passages
 *     are numbered, the model returns the order it finds most relevant to
 *     the query, and anything it leaves out keeps its fused order.
 *
 * Both are additive: a reranker that errors or times out leaves the fused
 * order untouched, and no reranker is installed unless the CLI attaches
 * one (CHATCLI_KNOWLEDGE_RERANK).
 */
package ctxmgr

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// RerankCandidate is one passage offered to a Reranker: its fused score in
// [0,1] and the text the reranker may read (already clipped).
type RerankCandidate struct {
	ID       string
	FilePath string
	Content  string
	Score    float64
}

// Reranker reorders the head of the fused candidate pool. Implementations
// return the candidates best-first; they may drop none.
type Reranker interface {
	Name() string
	Rerank(ctx context.Context, query string, cands []RerankCandidate) ([]RerankCandidate, error)
}

const (
	// rerankPoolCap bounds how many fused candidates reach the reranker.
	rerankPoolCap = 24
	// rerankSnippetChars is how much of each passage a reranker sees.
	rerankSnippetChars = 480
	// lexicalRelativeFloor drops BM25 hits scoring under this share of the
	// best hit: on a corpus where the query barely matches, the tail of the
	// pool is noise that only a strong vector signal could rescue.
	lexicalRelativeFloor = 0.05
)

// MMRReranker is the keyless diversity reranker (maximal marginal
// relevance). Lambda in (0,1] weighs relevance against novelty: 1 keeps
// the fused order, 0.7 (the default) trades a little relevance for
// coverage.
type MMRReranker struct {
	Lambda float64
}

// Name implements Reranker.
func (MMRReranker) Name() string { return "mmr" }

// Rerank implements Reranker: greedy MMR over Jaccard token overlap.
func (r MMRReranker) Rerank(_ context.Context, _ string, cands []RerankCandidate) ([]RerankCandidate, error) {
	lambda := r.Lambda
	if lambda <= 0 || lambda > 1 {
		lambda = 0.7
	}
	if len(cands) < 3 {
		return cands, nil
	}
	tokens := make([]map[string]struct{}, len(cands))
	for i, c := range cands {
		tokens[i] = rerankTokenSet(c.Content)
	}
	selected := make([]int, 0, len(cands))
	remaining := make([]int, 0, len(cands))
	for i := range cands {
		remaining = append(remaining, i)
	}
	for len(remaining) > 0 {
		best, bestVal := -1, -1.0
		for _, i := range remaining {
			maxSim := 0.0
			for _, s := range selected {
				if sim := jaccard(tokens[i], tokens[s]); sim > maxSim {
					maxSim = sim
				}
			}
			val := lambda*cands[i].Score - (1-lambda)*maxSim
			if val > bestVal {
				best, bestVal = i, val
			}
		}
		selected = append(selected, best)
		remaining = removeIndex(remaining, best)
	}
	out := make([]RerankCandidate, 0, len(cands))
	for _, i := range selected {
		out = append(out, cands[i])
	}
	return out, nil
}

func removeIndex(xs []int, v int) []int {
	for i, x := range xs {
		if x == v {
			return append(xs[:i], xs[i+1:]...)
		}
	}
	return xs
}

func rerankTokenSet(s string) map[string]struct{} {
	set := map[string]struct{}{}
	for _, tok := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r > 127)
	}) {
		if len(tok) >= 3 {
			set[tok] = struct{}{}
		}
	}
	return set
}

func jaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for t := range a {
		if _, ok := b[t]; ok {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// PromptFunc is the model call an LLMReranker makes: one prompt in, the
// raw completion out. The CLI supplies it bound to the session (or the
// compaction) client so this package stays free of LLM wiring.
type PromptFunc func(ctx context.Context, prompt string) (string, error)

// LLMReranker asks a model for a listwise order of the candidates.
type LLMReranker struct {
	Call    PromptFunc
	Timeout time.Duration
}

// Name implements Reranker.
func (LLMReranker) Name() string { return "llm" }

// ErrRerankUnavailable is returned when the reranker has no model call.
var ErrRerankUnavailable = errors.New("rerank: no model call configured")

// Rerank implements Reranker. The model's answer is parsed leniently (any
// sequence of 1-based numbers, first occurrence wins); candidates it does
// not mention keep their fused order after the ones it ranked.
func (r LLMReranker) Rerank(ctx context.Context, query string, cands []RerankCandidate) ([]RerankCandidate, error) {
	if r.Call == nil {
		return cands, ErrRerankUnavailable
	}
	if len(cands) < 2 {
		return cands, nil
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	answer, err := r.Call(callCtx, buildRerankPrompt(query, cands))
	if err != nil {
		return cands, err
	}
	order := parseRankedIndices(answer, len(cands))
	if len(order) == 0 {
		return cands, fmt.Errorf("rerank: no ranking in model answer")
	}
	seen := make(map[int]bool, len(cands))
	out := make([]RerankCandidate, 0, len(cands))
	for _, i := range order {
		if !seen[i] {
			seen[i] = true
			out = append(out, cands[i])
		}
	}
	for i := range cands {
		if !seen[i] {
			out = append(out, cands[i])
		}
	}
	return out, nil
}

func buildRerankPrompt(query string, cands []RerankCandidate) string {
	var b strings.Builder
	b.WriteString("You rank passages by relevance to a query. Reply with ONLY the passage numbers, best first, comma-separated (e.g. 3,1,2). Do not explain.\n\n")
	b.WriteString("Query: ")
	b.WriteString(strings.TrimSpace(query))
	b.WriteString("\n\nPassages:\n")
	for i, c := range cands {
		snippet := strings.TrimSpace(c.Content)
		if len(snippet) > rerankSnippetChars {
			snippet = snippet[:alignRuneBefore(snippet, rerankSnippetChars)]
		}
		fmt.Fprintf(&b, "[%d] (%s) %s\n\n", i+1, c.FilePath, strings.ReplaceAll(snippet, "\n", " "))
	}
	b.WriteString("Ranking:")
	return b.String()
}

var rerankNumberRe = regexp.MustCompile(`\d+`)

// parseRankedIndices extracts 1-based passage numbers within [1,n] from
// the model answer, returned 0-based in order of first appearance.
func parseRankedIndices(answer string, n int) []int {
	out := make([]int, 0, n)
	seen := map[int]bool{}
	for _, m := range rerankNumberRe.FindAllString(answer, -1) {
		v, err := strconv.Atoi(m)
		if err != nil || v < 1 || v > n || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v-1)
	}
	return out
}

// applyLexicalFloor drops BM25 hits under lexicalRelativeFloor of the top
// score. hits arrive sorted best-first.
func applyLexicalFloor(hits []lexHit) []lexHit {
	if len(hits) == 0 || hits[0].score <= 0 {
		return hits
	}
	floor := hits[0].score * lexicalRelativeFloor
	out := hits[:0:0]
	for _, h := range hits {
		if h.score >= floor {
			out = append(out, h)
		}
	}
	return out
}
