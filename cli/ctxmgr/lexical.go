/*
 * ChatCLI - Keyless lexical retrieval (BM25) for knowledge contexts.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The embedding-backed retrieval engine needs an API key (Voyage/OpenAI/
 * Bedrock); knowledge mode must work without one — the project's keyless-first
 * rule. This file is that floor: a small pure-Go BM25 index over the same
 * Segment grain the vector path uses. It is built in memory on demand (a 6MB
 * corpus tokenizes in well under a second) and combined with cosine scores by
 * the hybrid retriever when embeddings are available.
 */
package ctxmgr

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

// BM25 free parameters — the standard Robertson/Spärck Jones defaults; k1
// saturates term frequency, b scales length normalization.
const (
	bm25K1 = 1.2
	bm25B  = 0.75
)

// lexHit is one BM25 result: a segment index and its (unnormalized) score.
type lexHit struct {
	idx   int
	score float64
}

// lexicalIndex is an in-memory inverted index over a segment list.
type lexicalIndex struct {
	postings map[string]map[int]int // term → segment index → term frequency
	docLen   []int                  // tokens per segment
	avgLen   float64
}

// newLexicalIndex tokenizes every segment once and builds the inverted index.
func newLexicalIndex(segs []Segment) *lexicalIndex {
	idx := &lexicalIndex{
		postings: make(map[string]map[int]int),
		docLen:   make([]int, len(segs)),
	}
	var total int
	for i, s := range segs {
		terms := tokenizeLexical(s.Content)
		idx.docLen[i] = len(terms)
		total += len(terms)
		for _, t := range terms {
			m, ok := idx.postings[t]
			if !ok {
				m = make(map[int]int)
				idx.postings[t] = m
			}
			m[i]++
		}
	}
	if len(segs) > 0 {
		idx.avgLen = float64(total) / float64(len(segs))
	}
	return idx
}

// search scores the query against the index and returns up to k hits in
// descending score order. Ties break by ascending segment index so results
// are deterministic.
func (l *lexicalIndex) search(query string, k int) []lexHit {
	terms := tokenizeLexical(query)
	if len(terms) == 0 || len(l.docLen) == 0 || k <= 0 {
		return nil
	}
	n := float64(len(l.docLen))
	scores := make(map[int]float64)
	seen := make(map[string]struct{}, len(terms))
	for _, t := range terms {
		if _, dup := seen[t]; dup {
			continue // repeated query terms must not double-count idf
		}
		seen[t] = struct{}{}
		posting, ok := l.postings[t]
		if !ok {
			continue
		}
		df := float64(len(posting))
		idf := math.Log(1 + (n-df+0.5)/(df+0.5))
		for doc, tf := range posting {
			norm := 1 - bm25B + bm25B*float64(l.docLen[doc])/l.avgLen
			scores[doc] += idf * float64(tf) * (bm25K1 + 1) / (float64(tf) + bm25K1*norm)
		}
	}
	hits := make([]lexHit, 0, len(scores))
	for doc, s := range scores {
		hits = append(hits, lexHit{idx: doc, score: s})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].idx < hits[j].idx
	})
	if len(hits) > k {
		hits = hits[:k]
	}
	return hits
}

// tokenizeLexical lowercases and splits on non-alphanumeric runes, dropping
// one-rune tokens (noise in both prose and code). No stemming and no stopword
// list: BM25's idf already discounts ubiquitous terms, and staying
// language-neutral keeps pt-BR and English corpora equally searchable.
func tokenizeLexical(text string) []string {
	fields := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := fields[:0]
	for _, f := range fields {
		if len(f) > 1 {
			out = append(out, f)
		}
	}
	return out
}
