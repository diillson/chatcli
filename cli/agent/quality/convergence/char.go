/*
 * ChatCLI - Convergence: character-level scorer.
 *
 * The cheapest signal: compare length delta + character mismatches
 * up to min(len(a), len(b)). Identical → Similarity 1.0. This is a
 * refactor of the original `convergedRefine` heuristic, now exposed
 * as a proper Scorer so the Composite cascade can use it as a first
 * filter.
 *
 * Confidence is intentionally modest (0.3–0.6): char-level agreement
 * is a hint, not proof. Two semantically identical rewrites with
 * different wording will score low here and should be pushed further
 * down the cascade (Jaccard → Embedding).
 */
package convergence

import (
	"context"
	"time"
)

// CharScorer produces similarity based on character-level diff.
type CharScorer struct{}

// NewCharScorer builds a stateless char-level scorer.
func NewCharScorer() *CharScorer { return &CharScorer{} }

// Name returns "char".
func (*CharScorer) Name() string { return "char" }

// Score returns 1.0 for identical strings, linearly decreasing with
// character mismatches up to the min length, clamped at 0.
func (s *CharScorer) Score(_ context.Context, a, b string) (Score, error) {
	start := time.Now()
	la := []rune(a)
	lb := []rune(b)

	if len(la) == 0 && len(lb) == 0 {
		return Score{Similarity: 1, Confidence: 1, Cost: Cost{Duration: time.Since(start)}}, nil
	}

	maxLen := len(la)
	if len(lb) > maxLen {
		maxLen = len(lb)
	}
	if maxLen == 0 {
		return Score{Similarity: 0, Confidence: 0.3, Cost: Cost{Duration: time.Since(start)}}, nil
	}

	minLen := len(la)
	if len(lb) < minLen {
		minLen = len(lb)
	}
	mismatches := 0
	for i := 0; i < minLen; i++ {
		if la[i] != lb[i] {
			mismatches++
		}
	}
	// Length difference counts against similarity too — otherwise
	// "hello" vs "hello world" would score artificially high.
	lengthDiff := maxLen - minLen
	totalDiff := mismatches + lengthDiff

	sim := 1.0 - float64(totalDiff)/float64(maxLen)
	// Char-level gives strong signal at the extremes (identical or
	// wildly different) but weak signal in the middle where two
	// rewrites may mean the same thing with different surface form.
	// We encode that in Confidence: high when sim is very high or
	// very low, moderate in between.
	conf := charConfidence(sim)

	return Score{
		Similarity: clamp01(sim),
		Confidence: conf,
		Cost:       Cost{Duration: time.Since(start)},
	}, nil
}

func charConfidence(sim float64) float64 {
	// Smooth U-curve: 0.9 at extremes, 0.3 at sim=0.5.
	// conf = 0.3 + 0.6 * |2*sim - 1|
	d := 2*sim - 1
	if d < 0 {
		d = -d
	}
	return clamp01(0.3 + 0.6*d)
}
