/*
 * ChatCLI - Embedding provider abstraction (Phase 3b — HyDE vectors).
 *
 * The interface is deliberately minimal: providers convert a batch of
 * strings to fixed-dimension float32 vectors. Cosine similarity in
 * pure Go (no CGO, no external deps) backs the vector store, so the
 * whole stack works with `go build` on every supported platform.
 *
 * Providers wired in this package:
 *   - voyage  : Voyage AI (Anthropic-recommended, best quality/$)
 *   - openai  : OpenAI text-embedding-3-small (256/512/1024/1536 dim)
 *   - null    : default no-op (returned when no provider is configured)
 */
package embedding

import (
	"context"
	"fmt"
	"math"
	"strings"
)

// Provider produces fixed-dimension embeddings for a batch of texts.
//
// Implementations must be safe for concurrent use by multiple
// goroutines and must return either len(texts) vectors of equal
// dimensionality or a non-nil error. A nil texts slice returns
// (nil, nil) — by contract, an empty batch is not an error.
type Provider interface {
	// Name identifies the provider in logs and /config quality output.
	Name() string
	// Dimension returns the fixed dimensionality of every vector this
	// provider emits. Used by the vector store to pre-size buffers
	// and reject mismatched providers (e.g. switching from a 512-dim
	// to a 1024-dim provider mid-session).
	Dimension() int
	// Embed converts a batch of texts to vectors. Order is preserved.
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// CosineSimilarity returns the cosine similarity between two vectors
// of equal length, in [-1, 1]. NaN/Inf inputs yield 0.
func CosineSimilarity(a, b []float32) float32 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		x := float64(a[i])
		y := float64(b[i])
		if math.IsNaN(x) || math.IsNaN(y) || math.IsInf(x, 0) || math.IsInf(y, 0) {
			return 0
		}
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(na) * math.Sqrt(nb)))
}

// NormalizeName lower-cases and trims a provider name string from env
// (CHATCLI_EMBED_PROVIDER) so factory dispatch is forgiving of casing.
func NormalizeName(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// ErrUnknownProvider is returned by NewFromEnv / NewByName when the
// requested provider does not exist.
var ErrUnknownProvider = fmt.Errorf("embedding: unknown provider")
