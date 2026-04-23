/*
 * ChatCLI - Convergence scorers (enterprise Self-Refine upgrade).
 *
 * A convergence scorer compares two revisions of a draft and returns
 * a similarity score in [0, 1] plus a confidence in [0, 1]. The
 * RefineHook uses a Composite of scorers in a cheap-to-expensive
 * cascade: char-level first, then token-set Jaccard, then embedding
 * cosine, then (optional) LLM-as-judge. Short-circuit rules in the
 * composite ensure we only pay for the expensive signals on
 * borderline cases.
 *
 * The replacement for the previous char-level `convergedRefine`
 * handles semantic equivalence (same meaning, different words) that
 * the original heuristic couldn't detect — eliminating both:
 *   - false positives: "3x1" vs "3-1" with old code considered
 *     "diverged", burning a needless refine pass.
 *   - false negatives: accidental keyword swap producing different
 *     meaning with identical length was OK under the old heuristic.
 *
 * The package is standalone so it can be consumed by the RefineHook
 * and, eventually, by Verify or Plan stabilization checks.
 */
package convergence

import (
	"context"
	"time"
)

// Score reports how similar two strings are and how confident the
// scorer is in that judgment for this specific pair of inputs.
//
// Similarity is in [0, 1]: 1.0 = identical, 0.0 = completely
// dissimilar. Scorers MUST clamp out-of-range values before returning.
//
// Confidence is in [0, 1]: how trustworthy this scorer's similarity
// is for THIS pair. Example: the embedding scorer may emit high
// confidence for long English paragraphs and lower confidence for
// short code snippets that embedders struggle with.
type Score struct {
	Similarity float64
	Confidence float64

	// Cost tracks what the scorer consumed. Composite uses this to
	// decide whether budget is exhausted on borderline cases.
	Cost Cost

	// DegradedFrom is non-empty when this score was produced by a
	// fallback scorer (e.g. breaker open on embedding → jaccard).
	// Consumers bubble it up to logs/metrics for observability.
	DegradedFrom string
}

// Cost groups resource consumption so a Composite can budget.
type Cost struct {
	Duration     time.Duration
	TokensUsed   int
	EmbedQueries int
}

// Add returns a+b (useful for the cascade to accumulate cost across
// scorers).
func (c Cost) Add(b Cost) Cost {
	return Cost{
		Duration:     c.Duration + b.Duration,
		TokensUsed:   c.TokensUsed + b.TokensUsed,
		EmbedQueries: c.EmbedQueries + b.EmbedQueries,
	}
}

// Scorer computes a similarity + confidence score for a pair of
// revisions.
//
// Implementations must be safe for concurrent use by multiple
// goroutines. Scorers with expensive state (embed cache, LLM client)
// should be constructed once and shared.
type Scorer interface {
	// Name identifies the scorer in logs/metrics.
	Name() string
	// Score compares a (previous draft) with b (new draft). Must
	// respect ctx — long-running scorers honor cancellation.
	Score(ctx context.Context, a, b string) (Score, error)
}

// clamp01 clips v to [0, 1].
func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
