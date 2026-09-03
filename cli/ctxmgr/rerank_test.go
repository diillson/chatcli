/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package ctxmgr

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

func TestMMRReranker_PrefersCoverageOverDuplicates(t *testing.T) {
	cands := []RerankCandidate{
		{ID: "a", Content: "postgres connection pool exhausted under load pgbouncer", Score: 1.0},
		{ID: "a2", Content: "postgres connection pool exhausted under load pgbouncer tuning", Score: 0.98},
		{ID: "b", Content: "redis eviction policy allkeys-lru memory pressure", Score: 0.9},
		{ID: "a3", Content: "connection pool exhausted postgres pgbouncer retry", Score: 0.88},
	}
	out, err := MMRReranker{Lambda: 0.5}.Rerank(context.Background(), "pool", cands)
	if err != nil {
		t.Fatal(err)
	}
	if out[0].ID != "a" {
		t.Fatalf("best candidate must stay first, got %s", out[0].ID)
	}
	if out[1].ID != "b" {
		t.Fatalf("second slot must go to the novel passage, got %s (order %v)", out[1].ID, ids(out))
	}
	if len(out) != len(cands) {
		t.Fatalf("MMR must not drop candidates: %d != %d", len(out), len(cands))
	}
}

func ids(c []RerankCandidate) []string {
	out := make([]string, len(c))
	for i, x := range c {
		out[i] = x.ID
	}
	return out
}

func TestLLMReranker_ParsesLenientlyAndKeepsUnmentioned(t *testing.T) {
	cands := []RerankCandidate{{ID: "1"}, {ID: "2"}, {ID: "3"}, {ID: "4"}}
	r := LLMReranker{Call: func(_ context.Context, prompt string) (string, error) {
		if !strings.Contains(prompt, "[4]") {
			t.Fatalf("prompt must number the passages: %s", prompt)
		}
		return "Ranking: 3, 1, 9, 3", nil
	}}
	out, err := r.Rerank(context.Background(), "q", cands)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(ids(out), ","); got != "3,1,2,4" {
		t.Fatalf("order = %s, want 3,1,2,4", got)
	}
}

func TestLLMReranker_ErrorsKeepOrder(t *testing.T) {
	cands := []RerankCandidate{{ID: "1"}, {ID: "2"}}
	r := LLMReranker{Call: func(context.Context, string) (string, error) { return "", errors.New("boom") }}
	out, err := r.Rerank(context.Background(), "q", cands)
	if err == nil || strings.Join(ids(out), ",") != "1,2" {
		t.Fatalf("error must surface and order stay, got err=%v order=%v", err, ids(out))
	}
	if _, err := (LLMReranker{}).Rerank(context.Background(), "q", cands); !errors.Is(err, ErrRerankUnavailable) {
		t.Fatalf("missing call must report ErrRerankUnavailable, got %v", err)
	}
}

func TestApplyLexicalFloor_DropsNoiseTail(t *testing.T) {
	hits := []lexHit{{0, 10}, {1, 4}, {2, 0.6}, {3, 0.2}}
	out := applyLexicalFloor(hits)
	if len(out) != 3 || out[2].idx != 2 {
		t.Fatalf("hits under 5%% of the top must go, got %v", out)
	}
	if got := applyLexicalFloor(nil); got != nil {
		t.Fatal("nil stays nil")
	}
}

// recordingReranker reverses the head it receives so the effect is visible.
type recordingReranker struct{ calls int }

func (r *recordingReranker) Name() string { return "rec" }
func (r *recordingReranker) Rerank(_ context.Context, _ string, c []RerankCandidate) ([]RerankCandidate, error) {
	r.calls++
	out := make([]RerankCandidate, len(c))
	for i := range c {
		out[i] = c[len(c)-1-i]
	}
	return out, nil
}

func lexicalOnlyEngine(t *testing.T) *RetrievalEngine {
	t.Helper()
	return NewRetrievalEngine(nil, t.TempDir(), zap.NewNop())
}

func TestRetrieveHybrid_RerankerReordersHeadAndScoresSurvive(t *testing.T) {
	e := lexicalOnlyEngine(t)
	fc := &FileContext{ID: "c1", Name: "c1", Files: []utils.FileInfo{
		{Path: "a.md", Content: "alpha beta gamma\n"},
		{Path: "b.md", Content: "alpha beta\n"},
		{Path: "c.md", Content: "alpha\n"},
		{Path: "d.md", Content: "unrelated text here\n"},
	}}
	base, err := e.RetrieveHybridScored(context.Background(), fc, "alpha beta gamma", 3)
	if err != nil || len(base) != 3 {
		t.Fatalf("baseline: %v (%d hits)", err, len(base))
	}
	if base[0].Score < base[1].Score || base[0].Segment.FilePath != "a.md" {
		t.Fatalf("baseline must be best-first with a.md on top, got %+v", base)
	}
	rr := &recordingReranker{}
	e.SetReranker(rr)
	out, err := e.RetrieveHybridScored(context.Background(), fc, "alpha beta gamma", 3)
	if err != nil || len(out) != 3 || rr.calls != 1 {
		t.Fatalf("reranked: err=%v hits=%d calls=%d", err, len(out), rr.calls)
	}
	if out[0].Segment.FilePath != base[len(base)-1].Segment.FilePath {
		t.Fatalf("reranker order must be honored, got %s first", out[0].Segment.FilePath)
	}
	// The fused score stays attached to the passage, not to the position.
	for _, o := range out {
		for _, b := range base {
			if o.Segment.ID == b.Segment.ID && o.Score != b.Score {
				t.Fatalf("score for %s changed: %v != %v", o.Segment.FilePath, o.Score, b.Score)
			}
		}
	}
	e.SetReranker(nil)
	plain, _ := e.RetrieveHybrid(context.Background(), fc, "alpha beta gamma", 3)
	if plain[0].FilePath != "a.md" {
		t.Fatal("clearing the reranker restores the fused order")
	}
}

func TestSegmentFiles_BoundariesPreferStructureForV2Only(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 12; i++ {
		sb.WriteString("line of prose that is fairly long to fill the window quickly enough " + strings.Repeat("x", 10) + "\n")
	}
	sb.WriteString("## Heading two\n")
	for i := 0; i < 12; i++ {
		sb.WriteString("more prose after the heading that keeps going for a while longer here\n")
	}
	file := utils.FileInfo{Path: "doc.md", Type: ".md", Content: sb.String()}

	v1 := SegmentFiles([]utils.FileInfo{file}, SegmentOptions{MaxChars: 1200})
	v2 := SegmentFiles([]utils.FileInfo{file}, segmentOptionsFor(&FileContext{Metadata: map[string]string{segmenterMetaKey: segmenterV2}}, SegmentOptions{MaxChars: 1200}))
	if len(v1) < 2 || len(v2) < 2 {
		t.Fatalf("both segmenters must split: v1=%d v2=%d", len(v1), len(v2))
	}
	startsAtHeading := false
	for _, s := range v2 {
		if strings.HasPrefix(s.Content, "## Heading two") {
			startsAtHeading = true
		}
	}
	if !startsAtHeading {
		t.Fatalf("v2 must open a segment at the heading; got starts %q", segStarts(v2))
	}
	if segmentOptionsFor(&FileContext{}, SegmentOptions{}).Boundaries {
		t.Fatal("untagged (pre-existing) contexts must keep the fixed-window cut")
	}
}

func segStarts(segs []Segment) []string {
	out := make([]string, len(segs))
	for i, s := range segs {
		first := s.Content
		if n := strings.IndexByte(first, '\n'); n > 0 {
			first = first[:n]
		}
		out[i] = first
	}
	return out
}

func TestBoundaryScore_ByFileType(t *testing.T) {
	if boundaryScore("## Title", ".md") != 3 || boundaryScore("func Foo() {", ".go") != 3 || boundaryScore("", ".go") != 2 || boundaryScore("}", ".go") != 1 || boundaryScore("plain", ".md") != 0 {
		t.Fatal("boundary scores by file type are off")
	}
}

func TestKnowledgeSearch_CorpusWeightOrdersAcrossBases(t *testing.T) {
	m, err := NewManagerWithBasePath(t.TempDir(), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	m.AttachEmbeddingProvider(nil)
	mk := func(id, name, text string) {
		m.contexts[id] = &FileContext{ID: id, Name: name, Mode: ModeKnowledge, Files: []utils.FileInfo{{Path: name + ".md", Content: text}}}
	}
	mk("k1", "alpha-base", "deploy pipeline rollback steps\n")
	mk("k2", "beta-base", "deploy pipeline rollback steps\n")
	if err := m.AttachContextWithOptions("s", "k1", AttachOptions{Priority: 1}); err != nil {
		t.Fatal(err)
	}
	if err := m.AttachContextWithOptions("s", "k2", AttachOptions{Priority: 2, Weight: 1.5}); err != nil {
		t.Fatal(err)
	}
	hits, err := m.KnowledgeSearch(context.Background(), "s", "", "deploy rollback", 2)
	if err != nil || len(hits) != 2 {
		t.Fatalf("hits=%d err=%v", len(hits), err)
	}
	if hits[0].ContextName != "beta-base" || hits[0].Score <= hits[1].Score {
		t.Fatalf("weighted base must lead: %+v", hits)
	}
	if m.AttachWeight("s", "k1") != 1.0 || m.AttachWeight("s", "k2") != 1.5 || m.AttachWeight("s", "nope") != 1.0 {
		t.Fatal("AttachWeight must default to 1.0")
	}
}
