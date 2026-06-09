/*
 * ChatCLI - Long-term memory: blended fact ranking.
 *
 * The retrieval critique this file answers: the original HyDE path embedded
 * the query, found the cosine top-K, then *threw the cosine score away* and
 * re-ranked purely by lexical substring overlap. That discarded the single
 * most expensive and most informative signal we had.
 *
 * The blended ranker fuses three independent, complementary signals into one
 * score:
 *
 *   semantic — cosine similarity from the vector store (synonymy, paraphrase)
 *   lexical  — keyword/tag overlap (exact terms, identifiers, file names)
 *   temporal — recency × access-frequency decay (what the user actually uses)
 *
 * Each signal lives on a different, non-comparable scale (cosine ∈ [0,1],
 * lexical ∈ [0,~1.5], temporal unbounded), so we min-max normalize each across
 * the candidate set before the weighted sum. Normalization is what makes the
 * weights meaningful and provider-agnostic: a Voyage 1024-d cosine and an
 * OpenAI 1536-d cosine both land in [0,1] after normalization, so the same
 * RankWeights behave the same on all 14 supported backends.
 *
 * Additive fusion (not multiplicative) is deliberate: a fact surfaced only by
 * the vector store — zero lexical overlap, the exact synonym case keyword
 * search is blind to — must still be able to rank. A product would zero it out.
 */
package memory

// RankWeights controls how the blended retriever combines the three relevance
// signals. Each weight is a non-negative multiplier applied to a min-max
// normalized signal. Weights need not sum to 1 — ranking is invariant to a
// uniform rescale — but keeping them in [0,1] keeps tuning legible.
type RankWeights struct {
	Semantic float64 `json:"semantic"` // cosine similarity from the vector store
	Lexical  float64 `json:"lexical"`  // keyword/tag overlap
	Temporal float64 `json:"temporal"` // recency × access-frequency decay
}

// DefaultRankWeights leans semantic-first: we paid an LLM call (HyDE) and an
// embedding call to obtain the cosine signal, so it should drive the ranking,
// with lexical overlap as a strong precision anchor and temporal recency as a
// lighter tie-breaker.
func DefaultRankWeights() RankWeights {
	return RankWeights{Semantic: 0.55, Lexical: 0.30, Temporal: 0.15}
}

// normalized clamps negative weights to zero and falls back to the defaults
// when the caller zeroed every signal (which would otherwise rank everything
// equally). The result is safe to feed straight into blendCandidates.
func (w RankWeights) normalized() RankWeights {
	w.Semantic = clampNonNeg(w.Semantic)
	w.Lexical = clampNonNeg(w.Lexical)
	w.Temporal = clampNonNeg(w.Temporal)
	if w.Semantic == 0 && w.Lexical == 0 && w.Temporal == 0 {
		return DefaultRankWeights()
	}
	return w
}

func clampNonNeg(x float64) float64 {
	if x < 0 {
		return 0
	}
	return x
}

// candidate carries the three raw signals for one fact through the blend. The
// raw values are filled by the retriever; blendCandidates overwrites final.
type candidate struct {
	fact     *Fact
	semantic float64 // raw cosine in [0,1] (0 when the fact had no vector hit)
	lexical  float64 // raw keyword relevance (0 when no keyword matched)
	temporal float64 // raw temporal score (always present, > 0)
	final    float64 // fused score, set by blendCandidates
}

// blendCandidates min-max normalizes each signal across the candidate set and
// writes the weighted sum into each candidate's final field. It mutates the
// slice in place and is O(n) over the candidates.
//
// A signal whose values are constant across all candidates carries no ranking
// information (it shifts every score by the same amount), so we normalize it to
// zero rather than inventing a spread — this keeps the blend honest and avoids
// a uniform signal silently dominating via its weight.
func blendCandidates(cands []*candidate, w RankWeights) {
	if len(cands) == 0 {
		return
	}

	semMin, semMax := minMax(cands, func(c *candidate) float64 { return c.semantic })
	lexMin, lexMax := minMax(cands, func(c *candidate) float64 { return c.lexical })
	tmpMin, tmpMax := minMax(cands, func(c *candidate) float64 { return c.temporal })

	for _, c := range cands {
		ns := normalize(c.semantic, semMin, semMax)
		nl := normalize(c.lexical, lexMin, lexMax)
		nt := normalize(c.temporal, tmpMin, tmpMax)
		c.final = w.Semantic*ns + w.Lexical*nl + w.Temporal*nt
	}
}

func minMax(cands []*candidate, sel func(*candidate) float64) (mn, mx float64) {
	mn, mx = sel(cands[0]), sel(cands[0])
	for _, c := range cands[1:] {
		v := sel(c)
		if v < mn {
			mn = v
		}
		if v > mx {
			mx = v
		}
	}
	return mn, mx
}

// normalize maps v into [0,1] given the observed [mn,mx] range. A degenerate
// range (all values equal) maps to 0: a constant signal can't discriminate, so
// it contributes nothing rather than an arbitrary constant.
func normalize(v, mn, mx float64) float64 {
	const eps = 1e-12
	span := mx - mn
	if span <= eps {
		return 0
	}
	return (v - mn) / span
}
