/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package vindex

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

// failAfterProvider answers n requests then fails; dim is what it emits.
type failAfterProvider struct {
	ok, calls int
	dim       int
	name      string
}

func (p *failAfterProvider) Name() string { return p.name }
func (p *failAfterProvider) Dimension() int {
	if p.calls == 0 {
		return 0 // like Ollama: unknown until the first embed
	}
	return p.dim
}
func (p *failAfterProvider) Embed(_ context.Context, texts []string) ([][]float32, error) {
	p.calls++
	if p.calls > p.ok {
		return nil, errors.New("503 provider down")
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		v := make([]float32, p.dim)
		v[0] = 1
		out[i] = v
	}
	return out, nil
}

func TestUpsert_PersistsTheChunksThatSucceeded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vec.json")
	p := &failAfterProvider{ok: 2, dim: 3, name: "fake:x"}
	idx := New(path, p)
	items := map[string]string{}
	for i := 0; i < UpsertBatch*3; i++ {
		items[fmt.Sprintf("id%03d", i)] = "text"
	}
	err := idx.Upsert(context.Background(), items)
	if err == nil {
		t.Fatal("the failed chunk must be reported")
	}
	if got := idx.Count(); got != UpsertBatch*2 {
		t.Fatalf("stored = %d, want the two successful chunks", got)
	}
	reloaded := New(path, &failAfterProvider{ok: 0, dim: 3, name: "fake:x"})
	if reloaded.Count() != UpsertBatch*2 {
		t.Fatalf("partial progress must be on disk: %d", reloaded.Count())
	}
}

func TestLoad_AdoptsTheProviderDimensionWhenDiskContradictsIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vec.json")
	first := New(path, &failAfterProvider{ok: 10, dim: 3, name: "ollama:m"})
	if err := first.Upsert(context.Background(), map[string]string{"a": "x", "b": "y"}); err != nil {
		t.Fatal(err)
	}
	// Same model name, different dimension (the model was swapped under
	// the same tag); the provider reports 0 until it embeds.
	swapped := New(path, &failAfterProvider{ok: 10, dim: 5, name: "ollama:m"})
	if swapped.Count() != 2 {
		t.Fatal("cache loads (dimension unknown yet)")
	}
	if err := swapped.Upsert(context.Background(), map[string]string{"c": "z"}); err != nil {
		t.Fatalf("first embed with a contradicting dimension must re-seed, not fail: %v", err)
	}
	if swapped.Count() != 1 {
		t.Fatalf("stale vectors must be discarded: size=%d", swapped.Count())
	}
	if len(swapped.MissingFor([]string{"a", "b", "c"})) != 2 {
		t.Fatal("old ids are missing again, the new one is stored")
	}
}

func TestProviderName_NilSafe(t *testing.T) {
	var none *Index
	if none.ProviderName() != "" || (&Index{}).ProviderName() != "" {
		t.Fatal("nil index or nil provider → empty name")
	}
	if idx := New(filepath.Join(t.TempDir(), "v.json"), &failAfterProvider{ok: 1, dim: 3, name: "fake:named"}); idx.ProviderName() != "fake:named" {
		t.Fatalf("name = %q", idx.ProviderName())
	}
}
