/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package ctxmgr

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/utils"
)

func TestNormalizeTerm_FoldAndStem(t *testing.T) {
	cases := []struct{ in, fold, stem string }{
		{"configuração", "configuracao", "configuracao"},
		{"configurações", "configuracoes", "configuracao"},
		{"deploys", "deploys", "deploy"},
		{"deploying", "deploying", "deploy"},
		{"queries", "queries", "query"},
		{"rapidamente", "rapidamente", "rapida"},
		{"servidores", "servidores", "servidor"},
		{"s3", "s3", "s3"},
		{"oauth2", "oauth2", "oauth2"},
		{"naïve", "naive", "naive"},
	}
	for _, c := range cases {
		if got := normalizeTerm(c.in, NormalizeFold); got != c.fold {
			t.Errorf("fold(%q) = %q, want %q", c.in, got, c.fold)
		}
		if got := normalizeTerm(c.in, NormalizeStem); got != c.stem {
			t.Errorf("stem(%q) = %q, want %q", c.in, got, c.stem)
		}
		if got := normalizeTerm(c.in, NormalizeOff); got != c.in {
			t.Errorf("off must be identity: %q → %q", c.in, got)
		}
	}
}

func TestLexicalIndex_NormalizationIsOptInAndFingerprinted(t *testing.T) {
	segs := []Segment{{ID: "a", Content: "A configuração do deploy fica no arquivo de ambiente"}, {ID: "b", Content: "unrelated text about cats"}}
	t.Setenv(KnowledgeNormalizeEnv, "off")
	if hits := newLexicalIndex(segs).search("configuracao deploys", 5); len(hits) != 0 {
		t.Fatalf("byte-exact by default: %v", hits)
	}
	t.Setenv(KnowledgeNormalizeEnv, "stem")
	if hits := newLexicalIndex(segs).search("configuracao deploys", 5); len(hits) != 1 || hits[0].idx != 0 {
		t.Fatalf("stem mode must match folded plurals: %v", hits)
	}
	fc := &FileContext{UpdatedAt: time.Unix(1_700_000_000, 0), FileCount: 1, TotalSize: 10}
	stemFP := contextFingerprint(fc)
	t.Setenv(KnowledgeNormalizeEnv, "off")
	if contextFingerprint(fc) == stemFP {
		t.Fatal("the normalization mode must be part of the fingerprint (reindex on change)")
	}
	if KnowledgeNormalizeMode() != NormalizeOff {
		t.Fatal("unknown/off → off")
	}
}

func TestRetrievedBlock_BudgetDedupAndCitations(t *testing.T) {
	seen := map[string]struct{}{}
	segs := []Segment{{ID: "x", FilePath: "a.md", StartLine: 1, EndLine: 3, Content: "one"}, {ID: "y", FilePath: "a.md", StartLine: 4, EndLine: 6, Content: "two"}}
	first := dedupSegments(segs, seen)
	if len(first) != 2 {
		t.Fatal("first attachment keeps everything")
	}
	again := dedupSegments([]Segment{{ID: "other", FilePath: "a.md", StartLine: 1, EndLine: 3, Content: "one"}, {ID: "z", FilePath: "b.md", StartLine: 1, EndLine: 1, Content: "three"}}, seen)
	if len(again) != 1 || again[0].ID != "z" {
		t.Fatalf("same content at the same place must dedup across attachments: %v", again)
	}
	big := []Segment{{ID: "1", Content: strings.Repeat("a", 500)}, {ID: "2", Content: strings.Repeat("b", 500)}, {ID: "3", Content: strings.Repeat("c", 500)}}
	if fit := fitSegments(big, 1300); len(fit) != 2 {
		t.Fatalf("budget keeps the leading passages that fit: %d", len(fit))
	}
	if fit := fitSegments(big, 400); len(fit) != 1 || !strings.HasSuffix(fit[0].Content, "…") {
		t.Fatalf("the first passage is truncated to fit: %v", fit)
	}
	if fitSegments(big, 0) != nil {
		t.Fatal("no budget, nothing")
	}
	block := FormatKnowledgeSegmentsBlock("kb", segs)
	if !strings.Contains(block, "cite as [a.md:1-3]") || !strings.Contains(block, "[path:start-end]") || Citation(segs[1]) != "[a.md:4-6]" {
		t.Fatalf("citations: %s", block)
	}
	m := &Manager{}
	if m.retrievedBudget() != DefaultRetrievedBudgetChars {
		t.Fatal("default budget")
	}
	m.SetRetrievedBudget(100)
	if m.retrievedBudget() != 100 {
		t.Fatal("override")
	}
}

// warmProvider counts the texts it embedded.
type warmProvider struct{ calls, texts int }

func (w *warmProvider) Name() string   { return "fake:warm" }
func (w *warmProvider) Dimension() int { return 3 }
func (w *warmProvider) Embed(_ context.Context, texts []string) ([][]float32, error) {
	w.calls++
	w.texts += len(texts)
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{1, 0, float32(i)}
	}
	return out, nil
}

func TestWarm_EmbedsOnlyMissingPassages(t *testing.T) {
	p := &warmProvider{}
	e := NewRetrievalEngine(p, t.TempDir(), nil)
	fc := &FileContext{ID: "c1", Name: "c1", UpdatedAt: time.Now(), Mode: ModeKnowledge}
	for i := 0; i < 5; i++ {
		fc.Files = append(fc.Files, utils.FileInfo{Path: "f" + string(rune('a'+i)) + ".md", Content: strings.Repeat("passage text ", 20), Size: 260, Type: "Markdown"})
	}
	fc.FileCount = len(fc.Files)
	n, err := e.Warm(context.Background(), fc)
	if err != nil || n == 0 || p.texts != n {
		t.Fatalf("warm: n=%d err=%v texts=%d", n, err, p.texts)
	}
	n2, err := e.Warm(context.Background(), fc)
	if err != nil || n2 != 0 || p.texts != n {
		t.Fatalf("second warm must embed nothing: n2=%d texts=%d", n2, p.texts)
	}
	off := NewRetrievalEngine(nil, t.TempDir(), nil)
	if n, _ := off.Warm(context.Background(), fc); n != 0 {
		t.Fatal("no provider → no-op")
	}
}
