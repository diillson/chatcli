package eval

import (
	"math"
	"testing"
)

const tol = 1e-9

func approx(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-4 {
		t.Errorf("%s = %.6f, want %.6f", name, got, want)
	}
}

func TestEvaluate_HandComputed(t *testing.T) {
	// Sample 1: relevant {A,B}, ranker returns [A, X, B] at k=3.
	//   hits=2 (A@1, B@3); recall=2/2=1; precision=2/3; MRR=1/1=1
	//   dcg = 1/log2(2) + 1/log2(4) = 1 + 0.5 = 1.5
	//   idcg(2,3) = 1/log2(2) + 1/log2(3) = 1 + 0.630930 = 1.630930
	//   ndcg = 1.5 / 1.630930 = 0.919721
	// Sample 2: relevant {C}, ranker returns [Y,Z,W] → all zero.
	// Sample 3: relevant {} → skipped entirely (N unchanged).
	rankers := map[string][]string{
		"q1": {"A", "X", "B"},
		"q2": {"Y", "Z", "W"},
		"q3": {"anything"},
	}
	rank := func(q string, k int) []string { return rankers[q] }

	samples := []Sample{
		{Query: "q1", Relevant: []string{"A", "B"}},
		{Query: "q2", Relevant: []string{"C"}},
		{Query: "q3", Relevant: nil},
	}

	m := Evaluate(rank, samples, 3)

	if m.N != 2 {
		t.Fatalf("N = %d, want 2 (empty-relevant sample must be skipped)", m.N)
	}
	approx(t, "recall@k", m.RecallAtK, (1.0+0.0)/2)
	approx(t, "precision@k", m.PrecisionAtK, (2.0/3.0+0.0)/2)
	approx(t, "MRR", m.MRR, (1.0+0.0)/2)
	approx(t, "nDCG@k", m.NDCGAtK, (0.919721+0.0)/2)
}

func TestEvaluate_PerfectRanking(t *testing.T) {
	rank := func(q string, k int) []string { return []string{"A", "B", "C"} }
	samples := []Sample{{Query: "q", Relevant: []string{"A", "B"}}}
	m := Evaluate(rank, samples, 3)
	approx(t, "recall@k", m.RecallAtK, 1.0)
	approx(t, "MRR", m.MRR, 1.0)
	approx(t, "nDCG@k", m.NDCGAtK, 1.0) // both relevant in the top-2 slots → ideal
}

func TestEvaluate_TotalMiss(t *testing.T) {
	rank := func(q string, k int) []string { return []string{"X", "Y"} }
	samples := []Sample{{Query: "q", Relevant: []string{"A"}}}
	m := Evaluate(rank, samples, 3)
	if m.RecallAtK != 0 || m.MRR != 0 || m.NDCGAtK != 0 {
		t.Fatalf("expected all-zero on total miss, got %s", m)
	}
}

func TestEvaluate_KClampAndTruncation(t *testing.T) {
	// Relevant item sits at position 4; k=3 must not see it.
	rank := func(q string, k int) []string { return []string{"a", "b", "c", "REL"} }
	samples := []Sample{{Query: "q", Relevant: []string{"REL"}}}
	if m := Evaluate(rank, samples, 3); m.RecallAtK != 0 {
		t.Fatalf("recall@3 should be 0 when relevant is at rank 4, got %v", m.RecallAtK)
	}
	if m := Evaluate(rank, samples, 4); math.Abs(m.RecallAtK-1.0) > tol {
		t.Fatalf("recall@4 should be 1, got %v", m.RecallAtK)
	}
	// k <= 0 is treated as 1.
	if m := Evaluate(rank, samples, 0); m.K != 1 {
		t.Fatalf("k<=0 should clamp to 1, got K=%d", m.K)
	}
}

func TestComparison_Improvement(t *testing.T) {
	base := Metrics{RecallAtK: 0.4, MRR: 0.3}
	cand := Metrics{RecallAtK: 0.9, MRR: 0.8, N: 7, K: 5}
	d := Comparison{Baseline: base, Candidate: cand}.Improvement()
	approx(t, "delta recall", d.RecallAtK, 0.5)
	approx(t, "delta MRR", d.MRR, 0.5)
	if d.N != 7 || d.K != 5 {
		t.Fatalf("improvement should carry candidate N/K, got N=%d K=%d", d.N, d.K)
	}
}
