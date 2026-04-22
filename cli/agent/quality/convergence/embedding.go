/*
 * ChatCLI - Convergence: embedding-based cosine scorer.
 *
 * Consumes an embedding.Provider (the same interface HyDE uses), so
 * wiring via cli.hyde_setup's factory is trivial — zero extra
 * infrastructure. Results are cached in an LRU with TTL and protected
 * by a per-scorer circuit breaker: a flaky provider doesn't stall
 * every refine pass.
 *
 * When the breaker is open, the scorer returns Score{} with a
 * Non-zero DegradedFrom so the Composite cascade knows to substitute
 * a cheaper signal (Jaccard) seamlessly.
 */
package convergence

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/diillson/chatcli/llm/embedding"
)

// EmbeddingScorerConfig bundles the knobs. Defaults() gives a
// production-ready baseline.
type EmbeddingScorerConfig struct {
	CacheSize        int
	CacheTTL         time.Duration
	BreakerThreshold int
	BreakerCoolDown  time.Duration
	CallTimeout      time.Duration // per Embed() call
}

// DefaultEmbeddingScorerConfig returns production defaults.
func DefaultEmbeddingScorerConfig() EmbeddingScorerConfig {
	return EmbeddingScorerConfig{
		CacheSize:        256,
		CacheTTL:         5 * time.Minute,
		BreakerThreshold: 3,
		BreakerCoolDown:  30 * time.Second,
		CallTimeout:      15 * time.Second,
	}
}

// EmbeddingScorer uses cosine similarity on provider-computed
// embeddings. Strongest signal for semantic equivalence.
type EmbeddingScorer struct {
	provider embedding.Provider
	cache    *embedCache
	breaker  *circuitBreaker
	cfg      EmbeddingScorerConfig
}

// NewEmbeddingScorer builds a scorer against the given provider.
// A nil provider is accepted; Score will return Score{} with
// DegradedFrom="no_provider" so the caller knows to skip.
func NewEmbeddingScorer(p embedding.Provider, cfg EmbeddingScorerConfig) *EmbeddingScorer {
	if cfg.CacheSize <= 0 && cfg.CacheTTL <= 0 {
		cfg = DefaultEmbeddingScorerConfig()
	}
	if cfg.CallTimeout <= 0 {
		cfg.CallTimeout = 15 * time.Second
	}
	return &EmbeddingScorer{
		provider: p,
		cache:    newEmbedCache(cfg.CacheSize, cfg.CacheTTL),
		breaker:  newBreaker(cfg.BreakerThreshold, cfg.BreakerCoolDown),
		cfg:      cfg,
	}
}

// Name returns "embedding".
func (*EmbeddingScorer) Name() string { return "embedding" }

// Available reports whether the scorer can currently serve a call
// (provider present + breaker allows). Composite uses this to decide
// whether to even attempt embedding.
func (s *EmbeddingScorer) Available() bool {
	return s.provider != nil && s.breaker.Allow()
}

// BreakerState exposes the breaker for metrics/diagnostics.
func (s *EmbeddingScorer) BreakerState() string { return s.breaker.State().String() }

// Score returns cosine similarity + high confidence. On provider
// error or breaker open, returns a degraded Score (Similarity=0,
// Confidence=0, DegradedFrom populated) with a non-nil error so the
// Composite knows to fall back.
func (s *EmbeddingScorer) Score(ctx context.Context, a, b string) (Score, error) {
	start := time.Now()
	if s.provider == nil {
		return Score{DegradedFrom: "no_provider"}, errors.New("embedding scorer: no provider configured")
	}
	if !s.breaker.Allow() {
		return Score{DegradedFrom: "breaker_open"}, errors.New("embedding scorer: breaker open")
	}

	va, err := s.lookup(ctx, a)
	if err != nil {
		s.breaker.RecordFailure()
		return Score{DegradedFrom: "provider_error"}, fmt.Errorf("embed a: %w", err)
	}
	vb, err := s.lookup(ctx, b)
	if err != nil {
		s.breaker.RecordFailure()
		return Score{DegradedFrom: "provider_error"}, fmt.Errorf("embed b: %w", err)
	}

	cos := embedding.CosineSimilarity(va, vb)
	// Cosine in [-1, 1]; we map to [0, 1] by (cos+1)/2 so that
	// "orthogonal" (unrelated) maps to 0.5, "identical" to 1.0,
	// "exactly opposite" to 0.0. Similarity-orthogonal is the
	// interesting boundary.
	sim := (float64(cos) + 1) / 2
	s.breaker.RecordSuccess()

	return Score{
		Similarity: clamp01(sim),
		Confidence: 0.9, // embedding has the highest confidence of any cheap scorer
		Cost: Cost{
			Duration:     time.Since(start),
			EmbedQueries: embedQueryCount(va, vb),
		},
	}, nil
}

// lookup fetches a single vector, using cache when available.
func (s *EmbeddingScorer) lookup(ctx context.Context, text string) ([]float32, error) {
	if v, ok := s.cache.Get(text); ok {
		return v, nil
	}
	callCtx, cancel := context.WithTimeout(ctx, s.cfg.CallTimeout)
	defer cancel()
	vs, err := s.provider.Embed(callCtx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vs) != 1 {
		return nil, fmt.Errorf("embedding scorer: expected 1 vector; got %d", len(vs))
	}
	s.cache.Put(text, vs[0])
	return vs[0], nil
}

// embedQueryCount returns how many vectors we actually had to request
// from the provider for this pair. Callers use this for cost
// accounting / budgeting. If both came from cache, this is 0.
func embedQueryCount(va, vb []float32) int {
	// We can't know from here whether a lookup hit the cache — the
	// cache hit path returns early and we skipped the HTTP call.
	// For now, report 2 (one per vector) as a conservative upper
	// bound. A future refactor could track cache hits explicitly.
	_, _ = va, vb
	return 2
}
