/*
 * ChatCLI - Generic cosine vector index tests.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package vindex

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/diillson/chatcli/llm/embedding"
)

// oneHotProvider embeds on the first byte: identical-prefix texts collide,
// different-prefix texts are near-orthogonal. Deterministic and CGO-free.
type oneHotProvider struct {
	name string
	dim  int
}

func (p *oneHotProvider) Name() string   { return p.name }
func (p *oneHotProvider) Dimension() int { return p.dim }
func (p *oneHotProvider) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, p.dim)
		if len(t) > 0 && int(t[0]) < p.dim {
			v[t[0]] = 1
		}
		v[0] += 0.05
		out[i] = v
	}
	return out, nil
}

func newProvider() *oneHotProvider { return &oneHotProvider{name: "onehot", dim: 128} }

func TestIndex_DisabledWithNullProvider(t *testing.T) {
	x := New(filepath.Join(t.TempDir(), "v.json"), embedding.NewNull())
	if x.Enabled() {
		t.Fatal("null provider must disable the index")
	}
	if err := x.Upsert(context.Background(), map[string]string{"a": "x"}); err != nil {
		t.Fatalf("disabled Upsert should be a no-op, got %v", err)
	}
	if x.Count() != 0 {
		t.Fatal("disabled index must stay empty")
	}
	if got := x.Search([]float32{1}, 3, 0); got != nil {
		t.Fatalf("disabled Search must be nil, got %v", got)
	}
}

func TestIndex_NilSafe(t *testing.T) {
	var x *Index
	if x.Enabled() || x.Count() != 0 || x.Has("a") || x.MissingFor([]string{"a"}) != nil {
		t.Fatal("nil index must be fully safe")
	}
}

func TestIndex_UpsertSearchFloorAndTopK(t *testing.T) {
	x := New(filepath.Join(t.TempDir(), "v.json"), newProvider())
	if err := x.Upsert(context.Background(), map[string]string{
		"alpha": "alpha text",
		"beta":  "beta text",
		"gamma": "gamma text",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if x.Count() != 3 {
		t.Fatalf("Count = %d, want 3", x.Count())
	}

	q, err := x.EmbedQuery(context.Background(), "alpha query")
	if err != nil {
		t.Fatalf("EmbedQuery: %v", err)
	}

	// No floor, k=3: alpha ranks first (identical prefix → cosine ~1).
	all := x.Search(q, 3, 0)
	if len(all) == 0 || all[0].ID != "alpha" {
		t.Fatalf("top hit should be alpha, got %v", all)
	}
	// Real floor rejects the near-orthogonal beta/gamma.
	strong := x.Search(q, 3, 0.5)
	if len(strong) != 1 || strong[0].ID != "alpha" {
		t.Fatalf("floor 0.5 should keep only alpha, got %v", strong)
	}
	// k caps the result count.
	if got := x.Search(q, 1, 0); len(got) != 1 {
		t.Fatalf("k=1 must cap at 1, got %d", len(got))
	}
}

func TestIndex_MissingForAndPrune(t *testing.T) {
	x := New(filepath.Join(t.TempDir(), "v.json"), newProvider())
	_ = x.Upsert(context.Background(), map[string]string{"a": "aaa", "b": "bbb"})

	missing := x.MissingFor([]string{"a", "b", "c"})
	if len(missing) != 1 || missing[0] != "c" {
		t.Fatalf("MissingFor wrong: %v", missing)
	}
	// Prune to keep only "a".
	x.Prune(map[string]struct{}{"a": {}})
	if x.Count() != 1 || !x.Has("a") || x.Has("b") {
		t.Fatalf("Prune should leave only a, count=%d", x.Count())
	}
}

func TestIndex_PersistAndReload(t *testing.T) {
	dir := t.TempDir()
	p := newProvider()
	x1 := New(filepath.Join(dir, "v.json"), p)
	_ = x1.Upsert(context.Background(), map[string]string{"a": "aaa", "b": "bbb"})

	x2 := New(filepath.Join(dir, "v.json"), p)
	if x2.Count() != 2 {
		t.Fatalf("reload should restore 2 entries, got %d", x2.Count())
	}
}

func TestIndex_ProviderChangeAutoMigrates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "v.json")
	x1 := New(path, &oneHotProvider{name: "alpha", dim: 64})
	_ = x1.Upsert(context.Background(), map[string]string{"a": "aaa"})

	// Reopen with a different provider, same dimension → must auto-clear.
	x2 := New(path, &oneHotProvider{name: "beta", dim: 64})
	if x2.Count() != 0 {
		t.Fatalf("provider switch must clear cache, count=%d", x2.Count())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("stale file should be removed, stat err=%v", err)
	}
}

func TestIndex_DimensionChangeAutoMigrates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "v.json")
	x1 := New(path, &oneHotProvider{name: "p", dim: 32})
	_ = x1.Upsert(context.Background(), map[string]string{"a": "aaa"})

	x2 := New(path, &oneHotProvider{name: "p", dim: 64})
	if x2.Count() != 0 {
		t.Fatalf("dimension switch must clear cache, count=%d", x2.Count())
	}
}

func TestIndex_ForgetRemoves(t *testing.T) {
	x := New(filepath.Join(t.TempDir(), "v.json"), newProvider())
	_ = x.Upsert(context.Background(), map[string]string{"a": "aaa", "b": "bbb"})
	x.Forget("a")
	if x.Count() != 1 || x.Has("a") {
		t.Fatalf("Forget should drop a, count=%d", x.Count())
	}
}

func TestIndex_ScoreAgainst(t *testing.T) {
	x := New(filepath.Join(t.TempDir(), "v.json"), newProvider())
	if err := x.Upsert(context.Background(), map[string]string{
		"alpha": "alpha text",
		"beta":  "beta text",
		"gamma": "gamma text",
	}); err != nil {
		t.Fatal(err)
	}
	qv, err := x.EmbedQuery(context.Background(), "alpha question")
	if err != nil {
		t.Fatal(err)
	}

	// Scores ONLY the requested ids; unknown ids are skipped silently.
	hits := x.ScoreAgainst(qv, []string{"alpha", "beta", "nope"}, 0)
	if len(hits) != 2 || hits[0].ID != "alpha" {
		t.Fatalf("hits = %+v, want alpha first and nope skipped", hits)
	}
	if hits[0].Score <= hits[1].Score {
		t.Fatalf("alpha must outscore beta: %+v", hits)
	}

	// gamma is in the index but not in the candidate set — never scored.
	for _, h := range hits {
		if h.ID == "gamma" {
			t.Fatal("non-candidate id leaked into the rerank")
		}
	}

	// The relevance floor applies per call.
	if got := x.ScoreAgainst(qv, []string{"beta"}, 0.99); len(got) != 0 {
		t.Fatalf("floor must filter weak candidates, got %+v", got)
	}

	// Disabled/empty inputs are nil-safe.
	if x.ScoreAgainst(nil, []string{"alpha"}, 0) != nil || x.ScoreAgainst(qv, nil, 0) != nil {
		t.Fatal("empty inputs must return nil")
	}
	off := New(filepath.Join(t.TempDir(), "v2.json"), embedding.NewNull())
	if off.ScoreAgainst(qv, []string{"alpha"}, 0) != nil {
		t.Fatal("disabled index must return nil")
	}
}
