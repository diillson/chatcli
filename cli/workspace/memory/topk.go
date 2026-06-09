/*
 * ChatCLI - Long-term memory: bounded top-K selection.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The vector store ranks fact_id → cosine over every stored vector. Sorting
 * the entire candidate set is O(n log n); we only ever want the top-K, so a
 * bounded min-heap gives the same answer in O(n log k) time and O(k) space.
 *
 * This is the difference between "fine for hundreds of facts" and "fine for
 * tens of thousands": the heap caps the work at k regardless of corpus size,
 * lifting the scan ceiling without dragging CGO / a vector DB into the build.
 */
package memory

import (
	"container/heap"
	"sort"
)

// scoredItem is one (id, score) pair flowing through top-K selection.
type scoredItem struct {
	id    string
	score float64
}

// minScoreHeap is a min-heap on score: the smallest score sits at the root so
// it can be evicted the instant a stronger candidate arrives.
type minScoreHeap []scoredItem

func (h minScoreHeap) Len() int           { return len(h) }
func (h minScoreHeap) Less(i, j int) bool { return h[i].score < h[j].score }
func (h minScoreHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *minScoreHeap) Push(x any)        { *h = append(*h, x.(scoredItem)) }
func (h *minScoreHeap) Pop() any {
	old := *h
	n := len(old)
	it := old[n-1]
	*h = old[:n-1]
	return it
}

// topKSelector keeps the k highest-scoring items observed during a single
// scan. Offer every candidate; read the winners back, sorted descending, with
// sortedDesc. Not safe for concurrent use — scope one to a single query.
type topKSelector struct {
	k int
	h minScoreHeap
}

// newTopKSelector builds a selector for the k best items. k < 1 is clamped to
// 1 so the selector always yields at least the single best candidate.
func newTopKSelector(k int) *topKSelector {
	if k < 1 {
		k = 1
	}
	return &topKSelector{k: k, h: make(minScoreHeap, 0, k+1)}
}

// offer feeds one candidate. While the heap is under capacity it is admitted
// unconditionally; once full, the candidate replaces the current weakest only
// if it strictly beats it. Ties keep the incumbent, which — combined with the
// id tie-break in sortedDesc — makes the output deterministic across runs and
// platforms (Windows/Linux/macOS map iteration order never leaks through).
func (t *topKSelector) offer(id string, score float64) {
	if t.h.Len() < t.k {
		heap.Push(&t.h, scoredItem{id: id, score: score})
		return
	}
	if t.h[0].score >= score {
		return
	}
	t.h[0] = scoredItem{id: id, score: score}
	heap.Fix(&t.h, 0)
}

// sortedDesc returns the retained items ordered by score descending, breaking
// ties by ascending id for stable, reproducible output. It does not drain the
// heap, so the selector stays reusable.
func (t *topKSelector) sortedDesc() []scoredItem {
	out := make([]scoredItem, t.h.Len())
	copy(out, t.h)
	sort.Slice(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].id < out[j].id
	})
	return out
}
