/*
 * ChatCLI - Long-term memory: retrieval evaluation harness.
 *
 * The critique this package answers: the memory RAG had no way to PROVE its
 * retrieval was any good. Sophisticated plumbing (HyDE, embeddings, cosine) sat
 * on top of a ranking nobody measured. You cannot improve — or defend against
 * regressions in — what you do not measure.
 *
 * This is a tiny, dependency-free, provider- and OS-agnostic harness: feed it a
 * Ranker closure and a labeled sample set and it returns the standard
 * information-retrieval metrics (recall@k, precision@k, MRR, nDCG@k), macro-
 * averaged across queries. It runs the SAME way against a deterministic test
 * provider in CI or against a live embedding backend in a benchmark — the
 * harness never imports a provider, so it stays neutral across all 14 of them.
 *
 * Relevance is binary (a fact id is relevant or it is not), which is the right
 * model for this domain: a long-term-memory fact either answers the query or it
 * does not. Graded relevance can be layered on later without changing callers.
 */
package eval

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Sample is one labeled query: the set of fact ids that SHOULD be retrieved.
type Sample struct {
	Query    string
	Relevant []string
}

// Ranker returns fact ids ranked best-first for a query, capped at k. It is the
// single seam between the harness and whatever retrieval strategy is under test
// (keyword-only, semantic blend, …), so strategies are compared apples-to-apples.
type Ranker func(query string, k int) []string

// Metrics are the macro-averaged retrieval-quality numbers over a sample set.
// Macro-averaging (mean of per-query scores) weights every query equally,
// regardless of how many relevant facts it has — the right choice when some
// queries have one gold fact and others have several.
type Metrics struct {
	N            int     // samples actually scored (those with ≥1 relevant id)
	K            int     // cutoff used
	RecallAtK    float64 // mean fraction of relevant facts found in the top-k
	PrecisionAtK float64 // mean fraction of the top-k that was relevant
	MRR          float64 // mean reciprocal rank of the first relevant hit
	NDCGAtK      float64 // mean normalized discounted cumulative gain
}

// Evaluate runs rank over every sample and returns the aggregated metrics.
// Samples with no relevant ids are skipped (they are unanswerable and would
// divide by zero). k <= 0 is treated as 1.
func Evaluate(rank Ranker, samples []Sample, k int) Metrics {
	if k <= 0 {
		k = 1
	}
	var m Metrics
	m.K = k

	var sumRecall, sumPrecision, sumMRR, sumNDCG float64
	for _, s := range samples {
		rel := toSet(s.Relevant)
		if len(rel) == 0 {
			continue
		}
		m.N++

		ranked := rank(s.Query, k)
		if len(ranked) > k {
			ranked = ranked[:k]
		}

		hits := 0
		firstHitRank := 0
		dcg := 0.0
		for i, id := range ranked {
			if _, ok := rel[id]; ok {
				hits++
				if firstHitRank == 0 {
					firstHitRank = i + 1
				}
				dcg += 1.0 / math.Log2(float64(i+2)) // i+2: rank 1 → log2(2)=1
			}
		}

		sumRecall += float64(hits) / float64(len(rel))
		if denom := minInt(k, len(ranked)); denom > 0 {
			sumPrecision += float64(hits) / float64(denom)
		}
		if firstHitRank > 0 {
			sumMRR += 1.0 / float64(firstHitRank)
		}
		if idcg := idealDCG(len(rel), k); idcg > 0 {
			sumNDCG += dcg / idcg
		}
	}

	if m.N > 0 {
		n := float64(m.N)
		m.RecallAtK = sumRecall / n
		m.PrecisionAtK = sumPrecision / n
		m.MRR = sumMRR / n
		m.NDCGAtK = sumNDCG / n
	}
	return m
}

// idealDCG is the DCG of a perfect ranking: the min(relevant, k) top slots all
// filled with relevant facts. It is the denominator that normalizes nDCG into
// [0,1] regardless of how many relevant facts a query has.
func idealDCG(numRelevant, k int) float64 {
	n := minInt(numRelevant, k)
	idcg := 0.0
	for i := 0; i < n; i++ {
		idcg += 1.0 / math.Log2(float64(i+2))
	}
	return idcg
}

func toSet(ids []string) map[string]struct{} {
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id != "" {
			set[id] = struct{}{}
		}
	}
	return set
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// String renders the metrics as a single aligned line for logs and benchmark
// output.
func (m Metrics) String() string {
	return fmt.Sprintf(
		"n=%d k=%d  recall@k=%.4f  precision@k=%.4f  MRR=%.4f  nDCG@k=%.4f",
		m.N, m.K, m.RecallAtK, m.PrecisionAtK, m.MRR, m.NDCGAtK,
	)
}

// Comparison reports the delta between a baseline and a candidate strategy on
// the same sample set — the artifact an A/B (keyword vs. semantic blend)
// produces. Positive deltas mean the candidate retrieved better.
type Comparison struct {
	Baseline  Metrics
	Candidate Metrics
}

// Improvement returns the candidate-minus-baseline deltas for each metric.
func (c Comparison) Improvement() Metrics {
	return Metrics{
		N:            c.Candidate.N,
		K:            c.Candidate.K,
		RecallAtK:    c.Candidate.RecallAtK - c.Baseline.RecallAtK,
		PrecisionAtK: c.Candidate.PrecisionAtK - c.Baseline.PrecisionAtK,
		MRR:          c.Candidate.MRR - c.Baseline.MRR,
		NDCGAtK:      c.Candidate.NDCGAtK - c.Baseline.NDCGAtK,
	}
}

// String renders a human-readable A/B report.
func (c Comparison) String() string {
	var b strings.Builder
	b.WriteString("baseline : " + c.Baseline.String() + "\n")
	b.WriteString("candidate: " + c.Candidate.String() + "\n")
	d := c.Improvement()
	b.WriteString(fmt.Sprintf(
		"delta    : recall@k=%+.4f  precision@k=%+.4f  MRR=%+.4f  nDCG@k=%+.4f",
		d.RecallAtK, d.PrecisionAtK, d.MRR, d.NDCGAtK,
	))
	return b.String()
}

// Sort orders sample ids deterministically — exported only so callers building
// golden sets can normalize their fixtures.
func SortIDs(ids []string) { sort.Strings(ids) }
