/*
 * ChatCLI - Keyless BM25 ranking over ad-hoc document lists.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The knowledge corpus is not the only thing worth ranking without an API
 * key: saved sessions (and any future in-memory corpus) need the same
 * language-neutral scoring. This thin exported wrapper reuses the exact
 * tokenizer and BM25 scorer the knowledge segments use, so ranking behaves
 * identically across surfaces instead of each caller growing its own ad-hoc
 * relevance formula.
 */
package ctxmgr

// DocHit is one ranked document from RankDocsBM25: the input slice index and
// its (unnormalized) BM25 score.
type DocHit struct {
	Index int
	Score float64
}

// RankDocsBM25 builds a transient BM25 index over docs and returns up to k
// hits in descending score order (ties break by ascending index, so results
// are deterministic). Documents that share no term with the query are absent
// from the result. The index is throwaway by design — session-sized corpora
// tokenize in milliseconds, and callers with a persistent corpus should use
// the knowledge store instead.
func RankDocsBM25(docs []string, query string, k int) []DocHit {
	if len(docs) == 0 || k <= 0 {
		return nil
	}
	segs := make([]Segment, len(docs))
	for i, d := range docs {
		segs[i] = Segment{Content: d}
	}
	hits := newLexicalIndex(segs).search(query, k)
	out := make([]DocHit, len(hits))
	for i, h := range hits {
		out[i] = DocHit{Index: h.idx, Score: h.score}
	}
	return out
}
