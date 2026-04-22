/*
 * ChatCLI - Convergence: cascade composite scorer.
 *
 * The Composite runs scorers in a configurable chain, short-
 * circuiting when a scorer returns a strong signal. This gives
 * enterprise-grade quality (embedding + LLM judgment available)
 * without paying for them on obvious cases.
 *
 * Default cascade (low → high cost):
 *   1. char      — ms; short-circuit when sim > 0.99 (identical)
 *                  or sim < 0.3 (obviously different).
 *   2. jaccard   — ms; short-circuit when sim > 0.95 AND
 *                  both docs have ≥10 tokens (high confidence).
 *   3. embedding — hundreds of ms + $; authoritative for semantic
 *                  similarity. Falls back to jaccard if unavailable.
 *
 * Strict vs permissive mode:
 *   - Strict: if the embedding scorer is unavailable, the cascade
 *     refuses to declare convergence (IsConverged false). Used for
 *     high-stakes refine workloads where false-positive convergence
 *     = silently shipping unpolished output.
 *   - Permissive: degrades to the next scorer, tagging the Score
 *     with DegradedFrom so logs/metrics note the fallback. Default.
 */
package convergence

import (
	"context"
	"errors"
	"time"
)

// CompositeConfig controls the cascade thresholds and behavior.
type CompositeConfig struct {
	// CharHighSim: char-level similarity above this short-circuits
	// as "converged". Default 0.99.
	CharHighSim float64
	// CharLowSim: char-level similarity below this short-circuits
	// as "diverged" (so we skip the expensive scorers). Default 0.3.
	CharLowSim float64
	// JaccardHighSim: jaccard above this + MinTokensForJaccard tokens
	// short-circuits as "converged". Default 0.95.
	JaccardHighSim        float64
	MinTokensForJaccard   int
	// EmbeddingConvergedAt: final embedding similarity threshold.
	// Default 0.92.
	EmbeddingConvergedAt float64
	// Strict: in strict mode, missing embedding provider/breaker
	// open → IsConverged returns false. In permissive (default),
	// the cascade falls back to jaccard.
	Strict bool
	// Budget bounds the total cascade cost per Check call. 0 → no bound.
	Budget time.Duration
}

// DefaultCompositeConfig returns production defaults.
func DefaultCompositeConfig() CompositeConfig {
	return CompositeConfig{
		CharHighSim:          0.99,
		CharLowSim:           0.3,
		JaccardHighSim:       0.95,
		MinTokensForJaccard:  10,
		EmbeddingConvergedAt: 0.92,
		Strict:               false,
		Budget:               0,
	}
}

// Composite chains scorers under short-circuit rules.
type Composite struct {
	char    Scorer
	jaccard Scorer
	embed   *EmbeddingScorer // concrete type so we can check Available()
	cfg     CompositeConfig
}

// NewComposite builds a cascade. Any scorer may be nil; the cascade
// skips nil entries.
func NewComposite(char Scorer, jaccard Scorer, embed *EmbeddingScorer, cfg CompositeConfig) *Composite {
	return &Composite{char: char, jaccard: jaccard, embed: embed, cfg: cfg}
}

// CheckResult carries both the final verdict and the trail of scores
// the cascade visited. Useful for logs/metrics and for the caller
// (RefineHook) to decide whether to also record refine_low_quality
// metadata.
type CheckResult struct {
	Converged bool
	Score     Score        // the score that drove the decision
	Trail     []TrailEntry // every scorer we invoked
	TotalCost Cost
}

// TrailEntry is one step in the cascade.
type TrailEntry struct {
	Scorer string
	Score  Score
	Err    error
}

// Check returns whether two drafts have converged under the cascade
// rules. Errors from individual scorers are NOT fatal — the cascade
// proceeds to the next scorer and logs the failure in the trail.
func (c *Composite) Check(ctx context.Context, a, b string) (CheckResult, error) {
	res := CheckResult{}
	deadline := time.Time{}
	if c.cfg.Budget > 0 {
		deadline = time.Now().Add(c.cfg.Budget)
	}

	// 1. Char scorer — cheapest, short-circuits at extremes.
	if c.char != nil && !budgetExpired(deadline) {
		cs, err := c.char.Score(ctx, a, b)
		res.Trail = append(res.Trail, TrailEntry{Scorer: c.char.Name(), Score: cs, Err: err})
		res.TotalCost = res.TotalCost.Add(cs.Cost)
		if err == nil {
			if cs.Similarity >= c.cfg.CharHighSim {
				res.Converged = true
				res.Score = cs
				return res, nil
			}
			if cs.Similarity < c.cfg.CharLowSim {
				res.Converged = false
				res.Score = cs
				return res, nil
			}
		}
	}

	// 2. Jaccard — handles reordering/synonym-less rewrites.
	var jaccardScore *Score
	if c.jaccard != nil && !budgetExpired(deadline) {
		js, err := c.jaccard.Score(ctx, a, b)
		res.Trail = append(res.Trail, TrailEntry{Scorer: c.jaccard.Name(), Score: js, Err: err})
		res.TotalCost = res.TotalCost.Add(js.Cost)
		if err == nil {
			jaccardScore = &js
			// Short-circuit only when confidence is high enough.
			if js.Similarity >= c.cfg.JaccardHighSim && js.Confidence >= 0.6 {
				res.Converged = true
				res.Score = js
				return res, nil
			}
		}
	}

	// 3. Embedding — authoritative when available.
	if c.embed != nil && !budgetExpired(deadline) {
		if c.embed.Available() {
			es, err := c.embed.Score(ctx, a, b)
			res.Trail = append(res.Trail, TrailEntry{Scorer: c.embed.Name(), Score: es, Err: err})
			res.TotalCost = res.TotalCost.Add(es.Cost)
			if err == nil {
				res.Converged = es.Similarity >= c.cfg.EmbeddingConvergedAt
				res.Score = es
				return res, nil
			}
			// Embedding erred mid-call → fall back to jaccard below.
		} else if c.cfg.Strict {
			// Strict mode + embed unavailable → refuse to declare
			// convergence. Caller should interpret as "not yet".
			res.Converged = false
			res.Score = Score{DegradedFrom: "embedding_unavailable"}
			return res, errors.New("composite: embedding unavailable in strict mode")
		}
	}

	// Fallback path — prefer jaccard if we have it; else char.
	if jaccardScore != nil {
		res.Converged = jaccardScore.Similarity >= c.cfg.JaccardHighSim
		res.Score = *jaccardScore
		res.Score.DegradedFrom = "embedding_to_jaccard"
		return res, nil
	}
	// Ultimate fallback: char scorer's last word (even if inconclusive).
	if len(res.Trail) > 0 {
		last := res.Trail[0].Score
		res.Converged = last.Similarity >= c.cfg.CharHighSim
		res.Score = last
		res.Score.DegradedFrom = "char_only"
	}
	return res, nil
}

func budgetExpired(deadline time.Time) bool {
	if deadline.IsZero() {
		return false
	}
	return time.Now().After(deadline)
}
