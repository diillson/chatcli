/*
 * ChatCLI - Convergence: token-set Jaccard scorer.
 *
 * Jaccard measures |A ∩ B| / |A ∪ B| on the sets of lowercased,
 * whitespace-split, stop-word-filtered tokens. Catches the common
 * case of "same keywords, reordered sentences" that char-level misses.
 *
 * Stop words are pulled from a small built-in list; callers that want
 * something richer (stemming, language-aware stopwords) can subclass
 * by implementing Scorer themselves.
 */
package convergence

import (
	"context"
	"strings"
	"time"
)

// JaccardScorer computes token-set Jaccard similarity.
type JaccardScorer struct {
	stopWords map[string]struct{}
}

// NewJaccardScorer returns a scorer with an English stop-word filter.
// Pass extraStop to add domain-specific tokens to ignore.
func NewJaccardScorer(extraStop ...string) *JaccardScorer {
	sw := make(map[string]struct{}, len(defaultStopWords)+len(extraStop))
	for _, w := range defaultStopWords {
		sw[w] = struct{}{}
	}
	for _, w := range extraStop {
		sw[strings.ToLower(strings.TrimSpace(w))] = struct{}{}
	}
	return &JaccardScorer{stopWords: sw}
}

// Name returns "jaccard".
func (*JaccardScorer) Name() string { return "jaccard" }

// Score returns |A ∩ B| / |A ∪ B| on token sets. Empty strings →
// Similarity 1.0 (no meaningful difference) with modest confidence.
func (s *JaccardScorer) Score(_ context.Context, a, b string) (Score, error) {
	start := time.Now()
	setA := s.tokenize(a)
	setB := s.tokenize(b)

	if len(setA) == 0 && len(setB) == 0 {
		return Score{Similarity: 1, Confidence: 0.5, Cost: Cost{Duration: time.Since(start)}}, nil
	}
	if len(setA) == 0 || len(setB) == 0 {
		return Score{Similarity: 0, Confidence: 0.7, Cost: Cost{Duration: time.Since(start)}}, nil
	}

	// Intersection size.
	intersection := 0
	// Iterate the smaller set for cache friendliness.
	small, large := setA, setB
	if len(setB) < len(setA) {
		small, large = setB, setA
	}
	for tok := range small {
		if _, ok := large[tok]; ok {
			intersection++
		}
	}
	// Union = |A| + |B| - |A ∩ B|
	union := len(setA) + len(setB) - intersection
	sim := float64(intersection) / float64(union)
	// Confidence scales with corpus size: 5 tokens vs 50 tokens
	// inspires very different trust. Saturates around 0.9 for
	// documents with 50+ tokens each.
	conf := jaccardConfidence(len(setA), len(setB))

	return Score{
		Similarity: clamp01(sim),
		Confidence: conf,
		Cost:       Cost{Duration: time.Since(start)},
	}, nil
}

// tokenize splits s on whitespace and punctuation, lowercases, and
// filters stop-words and 1-char tokens. Returns a set (map with
// empty-struct values) for O(1) intersection.
func (s *JaccardScorer) tokenize(text string) map[string]struct{} {
	text = strings.ToLower(text)
	out := make(map[string]struct{})
	var current strings.Builder
	flush := func() {
		if current.Len() == 0 {
			return
		}
		tok := current.String()
		current.Reset()
		if len(tok) <= 1 {
			return
		}
		if _, skip := s.stopWords[tok]; skip {
			return
		}
		out[tok] = struct{}{}
	}
	for _, r := range text {
		// Treat anything that isn't a letter, digit, dash, or
		// underscore as a delimiter. That gets `hello-world`,
		// `snake_case`, and `v1.0` recognizable without also
		// matching punctuation as tokens.
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			current.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return out
}

func jaccardConfidence(nA, nB int) float64 {
	// Geometric mean of token counts, saturates at 50.
	// conf = min(sqrt(nA * nB) / 50, 0.95)
	prod := float64(nA) * float64(nB)
	if prod <= 0 {
		return 0
	}
	// Very cheap sqrt replacement: approximate via iterative.
	// For this scale (bounded ≤ 50), use a simple Newton step.
	x := prod
	for i := 0; i < 4; i++ {
		x = (x + prod/x) / 2
	}
	v := x / 50.0
	if v > 0.95 {
		return 0.95
	}
	return v
}

// defaultStopWords is a small, language-neutral-ish stop list.
// Keep it compact — bigger lists bias the scorer for long documents
// but distort short-sentence comparisons.
var defaultStopWords = []string{
	"a", "an", "the", "and", "or", "but", "if", "then", "else", "as",
	"at", "by", "for", "in", "of", "on", "to", "with", "from",
	"is", "was", "are", "were", "be", "been", "being",
	"i", "you", "he", "she", "we", "they", "it", "this", "that",
	"do", "does", "did", "not", "no",
	"o", "a", "os", "as", "e", "ou", "mas", "se", "para", "com",
	"por", "que", "um", "uma", "eu", "você", "ele", "ela", "nós", "eles", "elas",
}
