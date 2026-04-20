/*
 * ChatCLI - Null embedding provider.
 *
 * Returned by the factory when no provider is configured. Embed always
 * returns an error so callers know vectors are unavailable; callers
 * (HyDE, vector store) treat that as "fall back to keyword retrieval"
 * and never block on it.
 */
package embedding

import (
	"context"
	"fmt"
)

// Null is the default no-op provider. Dimension is 0 because no
// vectors are produced.
type Null struct{}

// NewNull returns the singleton-like null provider.
func NewNull() *Null { return &Null{} }

// Name returns "null".
func (*Null) Name() string { return "null" }

// Dimension returns 0 — there are no vectors.
func (*Null) Dimension() int { return 0 }

// Embed always errors.
func (*Null) Embed(_ context.Context, _ []string) ([][]float32, error) {
	return nil, fmt.Errorf("embedding: null provider is active — set CHATCLI_EMBED_PROVIDER to enable vectors")
}
