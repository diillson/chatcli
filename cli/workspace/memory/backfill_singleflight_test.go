package memory

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

// blockingProvider counts backfill Embed BATCHES (len > 1) and holds them
// until release is closed, so a test can observe how many concurrent
// backfills were launched. Single-text embeds — the synchronous EmbedQuery on
// the retrieval path — pass through immediately, otherwise the retrieval
// itself would deadlock the test.
type blockingProvider struct {
	dim     int
	calls   atomic.Int32
	release chan struct{}
}

func (p *blockingProvider) Name() string   { return "blocking-fake" }
func (p *blockingProvider) Dimension() int { return p.dim }
func (p *blockingProvider) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if len(texts) > 1 { // a backfill batch, not a query embed
		p.calls.Add(1)
		<-p.release
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		v := make([]float32, p.dim)
		v[0] = 1
		out[i] = v
	}
	return out, nil
}

// TestVectorBackfillIsSingleFlight pins the cost guard: every HyDE retrieval
// fires a detached backfill goroutine for facts lacking vectors. Concurrent
// retrievals (MoA participants, gateway turns) used to launch overlapping
// backfills embedding the SAME facts — double the embedding bill for zero
// benefit. Only one backfill may be in flight at a time.
func TestVectorBackfillIsSingleFlight(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, DefaultConfig(), zap.NewNop())
	p := &blockingProvider{dim: 64, release: make(chan struct{})}
	v := NewVectorIndex(dir, p, nil)
	m.AttachVectorIndex(v)

	m.RememberFact("service alpha listens on port 3001 behind nginx", "general")
	m.RememberFact("service beta stores queue state in redis db 2", "general")

	ctx := context.Background()
	// Two retrievals back-to-back: the first launches the backfill (blocked in
	// Embed); the second must observe it in flight and not launch another.
	m.GetRelevantContextWithHyDE(ctx, "which port does alpha use", []string{"alpha", "port"}, nil)
	m.GetRelevantContextWithHyDE(ctx, "where does beta store state", []string{"beta", "redis"}, nil)

	// Give any (incorrectly) spawned second goroutine time to reach Embed.
	time.Sleep(150 * time.Millisecond)
	got := p.calls.Load()
	close(p.release)

	// Wait for the detached backfill to finish persisting before the test's
	// TempDir cleanup runs (the goroutine outlives the retrieval on purpose).
	// The single-flight flag clears only after BackfillFacts returns — i.e.
	// after persistence — so waiting on Count alone races the final file
	// write against TempDir's RemoveAll.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && (m.backfillInFlight.Load() || v.Count() < 2) {
		time.Sleep(10 * time.Millisecond)
	}

	if got != 1 {
		t.Fatalf("%d concurrent backfill batches launched, want 1 (single-flight)", got)
	}
	if v.Count() != 2 {
		t.Fatalf("backfill did not complete: %d vectors stored, want 2", v.Count())
	}
}
