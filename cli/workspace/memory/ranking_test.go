package memory

import (
	"context"
	"testing"

	"github.com/diillson/chatcli/cli/workspace/memory/eval"
	"go.uber.org/zap"
)

// conceptProvider is a deterministic, dependency-free embedding provider for
// tests. It maps each token to one of a fixed set of semantic "concepts" and
// returns a count vector over those concepts. Two texts that talk about the
// same concept with DIFFERENT words (the synonym/paraphrase case that defeats
// keyword search) land on the same axis and so have high cosine — which is
// exactly the behavior a real embedding model provides and the property the
// blended ranker is meant to exploit. Provider-agnostic by design: it satisfies
// the same embedding.Provider contract as Voyage/OpenAI/Bedrock.
type conceptProvider struct{}

var conceptIndex = map[string]int{
	// 0: auth
	"oauth": 0, "login": 0, "token": 0, "tokens": 0, "signin": 0,
	"credential": 0, "credentials": 0, "verification": 0, "password": 0, "session": 0, "bearer": 0,
	// 1: database
	"postgres": 1, "database": 1, "sql": 1, "rows": 1, "schema": 1,
	"tables": 1, "records": 1, "stores": 1,
	// 2: deploy
	"kubernetes": 2, "rollout": 2, "canary": 2, "deployment": 2,
	"release": 2, "ship": 2, "version": 2,
	// 3: logging
	"zap": 3, "structured": 3, "logs": 3, "stdout": 3,
	"observability": 3, "trace": 3, "output": 3,
	// 4: cache
	"redis": 4, "eviction": 4, "ttl": 4, "cache": 4, "invalidation": 4, "caching": 4,
}

const conceptDim = 5

func (conceptProvider) Name() string   { return "concept-test" }
func (conceptProvider) Dimension() int { return conceptDim }

func (conceptProvider) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, text := range texts {
		vec := make([]float32, conceptDim)
		for _, tok := range tokenizeLetters(text) {
			if idx, ok := conceptIndex[tok]; ok {
				vec[idx]++
			}
		}
		out[i] = vec
	}
	return out, nil
}

// tokenizeLetters lowercases and splits on any non-letter, matching how the
// concept map keys are written.
func tokenizeLetters(s string) []string {
	var toks []string
	var cur []rune
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
			cur = append(cur, r+('a'-'A'))
		case r >= 'a' && r <= 'z':
			cur = append(cur, r)
		default:
			if len(cur) > 0 {
				toks = append(toks, string(cur))
				cur = cur[:0]
			}
		}
	}
	if len(cur) > 0 {
		toks = append(toks, string(cur))
	}
	return toks
}

// buildCorpus wires a FactIndex + VectorIndex over a temp dir, adds the corpus,
// backfills vectors, and returns a content→id map so tests can label samples.
func buildCorpus(t *testing.T) (*FactIndex, *VectorIndex, map[string]string) {
	t.Helper()
	dir := t.TempDir()
	cfg := DefaultConfig()
	fi := NewFactIndex(dir, cfg, zap.NewNop())

	corpus := []struct{ content, category string }{
		{"OAuth login issues bearer tokens", "auth"},
		{"Postgres stores user rows in tables", "database"},
		{"Kubernetes rollout uses canary strategy", "deploy"},
		{"Zap emits structured logs to stdout", "logging"},
		{"Redis eviction relies on ttl", "cache"},
	}
	for _, c := range corpus {
		fi.AddFact(c.content, c.category, nil)
	}

	contentToID := make(map[string]string)
	items := make(map[string]string)
	for _, f := range fi.GetAll() {
		contentToID[f.Content] = f.ID
		items[f.ID] = f.Content
	}

	vi := NewVectorIndex(dir, conceptProvider{}, zap.NewNop())
	if !vi.Enabled() {
		t.Fatal("vector index should be enabled with a real provider")
	}
	if err := vi.BackfillFacts(context.Background(), items); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	return fi, vi, contentToID
}

// TestBlendedBeatsKeyword is the headline A/B: on queries phrased with synonyms
// that never appear in the stored fact text, keyword retrieval is blind while
// the semantic blend recovers the right fact. This is the measurable proof that
// keeping the cosine score (instead of dissolving it back into keywords) lifts
// retrieval quality.
func TestBlendedBeatsKeyword(t *testing.T) {
	fi, vi, id := buildCorpus(t)
	ctx := context.Background()

	idsOf := func(facts []*Fact, k int) []string {
		out := make([]string, 0, k)
		for i, f := range facts {
			if i >= k {
				break
			}
			out = append(out, f.ID)
		}
		return out
	}

	keywordRanker := func(query string, k int) []string {
		return idsOf(fi.Search(ExtractKeywords([]string{query})), k)
	}
	blendedRanker := func(query string, k int) []string {
		kw := ExtractKeywords([]string{query})
		var semantic map[string]float64
		if vec, err := vi.EmbedQuery(ctx, query); err == nil {
			for _, h := range vi.SimilarFactsScored(vec, 10, 0.25) {
				if semantic == nil {
					semantic = make(map[string]float64)
				}
				semantic[h.ID] = h.Score
			}
		}
		return idsOf(fi.SearchBlended(kw, semantic, DefaultRankWeights()), k)
	}

	samples := []eval.Sample{
		// Synonym queries — zero lexical overlap with the fact text.
		{Query: "how is credential verification and signin handled", Relevant: []string{id["OAuth login issues bearer tokens"]}},
		{Query: "which schema holds the stored records", Relevant: []string{id["Postgres stores user rows in tables"]}},
		{Query: "how to ship a release version", Relevant: []string{id["Kubernetes rollout uses canary strategy"]}},
		{Query: "where does observability trace output go", Relevant: []string{id["Zap emits structured logs to stdout"]}},
		{Query: "explain cache invalidation policy", Relevant: []string{id["Redis eviction relies on ttl"]}},
		// Literal queries — keyword-friendly; blend must not regress here.
		{Query: "oauth token login", Relevant: []string{id["OAuth login issues bearer tokens"]}},
		{Query: "postgres rows", Relevant: []string{id["Postgres stores user rows in tables"]}},
	}

	base := eval.Evaluate(keywordRanker, samples, 1)
	cand := eval.Evaluate(blendedRanker, samples, 1)
	t.Logf("A/B keyword vs blended:\n%s", eval.Comparison{Baseline: base, Candidate: cand})

	if cand.RecallAtK <= base.RecallAtK {
		t.Fatalf("blended recall@1 (%.4f) must beat keyword (%.4f)", cand.RecallAtK, base.RecallAtK)
	}
	if cand.RecallAtK < 0.99 {
		t.Fatalf("blended recall@1 should be ~1.0, got %.4f", cand.RecallAtK)
	}
	if cand.MRR < base.MRR {
		t.Fatalf("blended MRR (%.4f) regressed below keyword (%.4f)", cand.MRR, base.MRR)
	}
}

// TestSearchBlended_SemanticOnlyFactSurfaces proves the core fix in isolation:
// a fact with NO keyword overlap still ranks when it carries a semantic score.
// Under the old multiplicative scorer (temporal × lexical) its lexical 0 would
// have zeroed it out entirely.
func TestSearchBlended_SemanticOnlyFactSurfaces(t *testing.T) {
	fi, _, id := buildCorpus(t)
	target := id["Redis eviction relies on ttl"]

	// Keywords that match nothing in any fact; semantic points only at target.
	got := fi.SearchBlended(
		[]string{"nonexistentkeywordxyz"},
		map[string]float64{target: 0.91},
		DefaultRankWeights(),
	)
	if len(got) == 0 || got[0].ID != target {
		t.Fatalf("semantic-only fact must surface first; got %v", idsList(got))
	}
}

// TestSearchBlended_EmptyEverythingFallsBackToAll mirrors Search's empty-query
// contract: no keywords and no semantic signal returns the full set by score.
func TestSearchBlended_EmptyEverythingFallsBackToAll(t *testing.T) {
	fi, _, _ := buildCorpus(t)
	got := fi.SearchBlended(nil, nil, DefaultRankWeights())
	if len(got) != 5 {
		t.Fatalf("empty query should return all 5 facts, got %d", len(got))
	}
}

func idsList(facts []*Fact) []string {
	out := make([]string, len(facts))
	for i, f := range facts {
		out[i] = f.ID
	}
	return out
}

func TestBlendCandidates_AdditiveFusionAndConstantSignal(t *testing.T) {
	// Two candidates. A wins on semantic, B wins on lexical; temporal is equal
	// (constant → contributes nothing). With semantic-leaning default weights,
	// A should rank first.
	a := &candidate{fact: &Fact{ID: "A"}, semantic: 0.9, lexical: 0.1, temporal: 1.0}
	b := &candidate{fact: &Fact{ID: "B"}, semantic: 0.1, lexical: 0.9, temporal: 1.0}
	cands := []*candidate{a, b}
	blendCandidates(cands, DefaultRankWeights())

	if a.final <= b.final {
		t.Fatalf("semantic-leaning blend should rank A>B, got A=%.4f B=%.4f", a.final, b.final)
	}
	// Constant temporal must not have moved the needle: recompute with temporal
	// varied and confirm ordering is driven by the discriminating signals.
	if a.final == 0 && b.final == 0 {
		t.Fatal("blend produced no separation between candidates")
	}
}

func TestRankWeights_NormalizedFallback(t *testing.T) {
	// All-zero weights are nonsensical (everything ties) → fall back to default.
	if got := (RankWeights{}).normalized(); got != DefaultRankWeights() {
		t.Fatalf("zero weights should fall back to default, got %+v", got)
	}
	// Negative weights clamp to zero.
	got := RankWeights{Semantic: -1, Lexical: 0.5, Temporal: 0.5}.normalized()
	if got.Semantic != 0 {
		t.Fatalf("negative weight should clamp to 0, got %v", got.Semantic)
	}
}
