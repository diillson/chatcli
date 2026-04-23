package convergence

import (
	"context"
	"testing"
	"time"
)

func TestCharScorer_Identical(t *testing.T) {
	s := NewCharScorer()
	score, err := s.Score(context.Background(), "hello world", "hello world")
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if score.Similarity != 1.0 {
		t.Fatalf("identical should be 1.0; got %f", score.Similarity)
	}
}

func TestCharScorer_AllDifferent(t *testing.T) {
	s := NewCharScorer()
	score, _ := s.Score(context.Background(), "abcdef", "zyxwvu")
	if score.Similarity > 0.2 {
		t.Fatalf("completely different should be near 0; got %f", score.Similarity)
	}
}

func TestCharScorer_LengthDiffMatters(t *testing.T) {
	s := NewCharScorer()
	// Same prefix but one is 2x as long.
	score, _ := s.Score(context.Background(), "hello", "hello, this is a much longer text")
	if score.Similarity > 0.6 {
		t.Fatalf("length differences should lower similarity; got %f", score.Similarity)
	}
}

func TestJaccardScorer_SameWordsDifferentOrder(t *testing.T) {
	s := NewJaccardScorer()
	a := "the quick brown fox jumps over lazy dog"
	b := "lazy dog jumps over the quick brown fox"
	score, _ := s.Score(context.Background(), a, b)
	if score.Similarity < 0.99 {
		t.Fatalf("reordered sentences must score ~1.0; got %f", score.Similarity)
	}
}

func TestJaccardScorer_SynonymsScoreLow(t *testing.T) {
	s := NewJaccardScorer()
	a := "the quick brown fox"
	b := "swift tawny canine"
	score, _ := s.Score(context.Background(), a, b)
	if score.Similarity > 0.2 {
		t.Fatalf("no shared tokens should score near 0; got %f", score.Similarity)
	}
}

func TestJaccardScorer_IgnoresStopwords(t *testing.T) {
	s := NewJaccardScorer()
	a := "cat"
	b := "the cat"
	// After stopword filter both reduce to {"cat"}, so identical sets.
	score, _ := s.Score(context.Background(), a, b)
	if score.Similarity != 1.0 {
		t.Fatalf("stopword-only difference should score 1.0; got %f", score.Similarity)
	}
}

func TestJaccardScorer_CaseInsensitive(t *testing.T) {
	s := NewJaccardScorer()
	score, _ := s.Score(context.Background(), "Go Programming", "go programming")
	if score.Similarity != 1.0 {
		t.Fatalf("case differences should not matter; got %f", score.Similarity)
	}
}

func TestEmbedCache_BasicGetPut(t *testing.T) {
	c := newEmbedCache(4, 0) // no TTL
	v := []float32{1, 2, 3}
	c.Put("hello", v)
	got, ok := c.Get("hello")
	if !ok {
		t.Fatal("expected cache hit")
	}
	if len(got) != 3 || got[0] != 1 {
		t.Fatalf("wrong vector returned: %v", got)
	}
}

func TestEmbedCache_LRUEviction(t *testing.T) {
	c := newEmbedCache(2, 0)
	c.Put("a", []float32{1})
	c.Put("b", []float32{2})
	c.Put("c", []float32{3}) // evicts "a"
	if _, ok := c.Get("a"); ok {
		t.Fatal("oldest entry should have been evicted")
	}
	if _, ok := c.Get("b"); !ok {
		t.Fatal("b should still be cached")
	}
}

func TestBreaker_TripsAfterThreshold(t *testing.T) {
	// CoolDown must be long enough that the Allow() check right after
	// tripping still returns false — the race detector's overhead can
	// easily blow past sub-microsecond durations.
	b := newBreaker(3, time.Minute)
	for i := 0; i < 3; i++ {
		b.RecordFailure()
	}
	if b.Allow() {
		t.Fatal("breaker should have tripped after 3 failures")
	}
	if b.State() != breakerOpen {
		t.Fatalf("expected open; got %s", b.State())
	}
}

func TestBreaker_SuccessResets(t *testing.T) {
	b := newBreaker(3, time.Minute)
	b.RecordFailure()
	b.RecordFailure()
	b.RecordSuccess()
	// 2 fails + success should reset counter; 2 more fails must NOT trip.
	b.RecordFailure()
	b.RecordFailure()
	if !b.Allow() {
		t.Fatalf("success should have reset counter; state=%s", b.State())
	}
}

// ─── composite cascade ────────────────────────────────────────────────────

type stubScorer struct {
	name   string
	score  Score
	err    error
	called *int
}

func (s *stubScorer) Name() string { return s.name }
func (s *stubScorer) Score(_ context.Context, _, _ string) (Score, error) {
	if s.called != nil {
		*s.called++
	}
	return s.score, s.err
}

func TestComposite_CharHighEarlyExits(t *testing.T) {
	charCalls, jacCalls := 0, 0
	char := &stubScorer{name: "char", score: Score{Similarity: 1.0, Confidence: 0.9}, called: &charCalls}
	jac := &stubScorer{name: "jaccard", called: &jacCalls}
	c := NewComposite(char, jac, nil, DefaultCompositeConfig())

	r, err := c.Check(context.Background(), "x", "x")
	if err != nil {
		t.Fatal(err)
	}
	if !r.Converged {
		t.Fatal("identical strings must converge")
	}
	if jacCalls != 0 {
		t.Fatalf("jaccard should be skipped when char is decisive; called %d", jacCalls)
	}
}

func TestComposite_CharLowEarlyExitsAsDiverged(t *testing.T) {
	charCalls, jacCalls := 0, 0
	char := &stubScorer{name: "char", score: Score{Similarity: 0.1, Confidence: 0.9}, called: &charCalls}
	jac := &stubScorer{name: "jaccard", called: &jacCalls}
	c := NewComposite(char, jac, nil, DefaultCompositeConfig())

	r, _ := c.Check(context.Background(), "x", "completely different")
	if r.Converged {
		t.Fatal("obviously-different strings must not converge")
	}
	if jacCalls != 0 {
		t.Fatalf("jaccard should be skipped on clear divergence; called %d", jacCalls)
	}
}

func TestComposite_FallsThroughToJaccard(t *testing.T) {
	charCalls, jacCalls := 0, 0
	char := &stubScorer{name: "char", score: Score{Similarity: 0.5, Confidence: 0.3}, called: &charCalls}
	jac := &stubScorer{name: "jaccard", score: Score{Similarity: 0.97, Confidence: 0.8}, called: &jacCalls}
	c := NewComposite(char, jac, nil, DefaultCompositeConfig())

	r, _ := c.Check(context.Background(), "x", "y")
	if jacCalls != 1 {
		t.Fatalf("jaccard should be invoked on borderline; called %d", jacCalls)
	}
	if !r.Converged {
		t.Fatal("high jaccard should declare convergence")
	}
}

func TestComposite_StrictModeRefusesWithoutEmbedding(t *testing.T) {
	char := &stubScorer{name: "char", score: Score{Similarity: 0.5, Confidence: 0.3}}
	jac := &stubScorer{name: "jaccard", score: Score{Similarity: 0.6, Confidence: 0.8}}
	embed := NewEmbeddingScorer(nil, DefaultEmbeddingScorerConfig()) // unavailable
	cfg := DefaultCompositeConfig()
	cfg.Strict = true
	c := NewComposite(char, jac, embed, cfg)

	r, err := c.Check(context.Background(), "x", "y")
	if err == nil {
		t.Fatal("strict mode must error when embedding is unavailable")
	}
	if r.Converged {
		t.Fatal("strict mode must not declare convergence without embedding")
	}
}

func TestComposite_Trail(t *testing.T) {
	char := &stubScorer{name: "char", score: Score{Similarity: 0.5, Confidence: 0.3}}
	jac := &stubScorer{name: "jaccard", score: Score{Similarity: 0.6, Confidence: 0.5}}
	c := NewComposite(char, jac, nil, DefaultCompositeConfig())

	r, _ := c.Check(context.Background(), "x", "y")
	if len(r.Trail) != 2 {
		t.Fatalf("trail should include both scorers; got %d", len(r.Trail))
	}
	if r.Trail[0].Scorer != "char" || r.Trail[1].Scorer != "jaccard" {
		t.Fatalf("trail order wrong: %+v", r.Trail)
	}
}
