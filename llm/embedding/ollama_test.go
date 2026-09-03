/*
 * ChatCLI - Ollama provider and instrumentation tests.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package embedding

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOllama_ProbesDimensionAndEmbeds(t *testing.T) {
	var gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			http.NotFound(w, r)
			return
		}
		var req ollamaRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotModel = req.Model
		out := make([][]float32, len(req.Input))
		for i := range req.Input {
			out[i] = []float32{0.1, 0.2, 0.3, 0.4}
		}
		_ = json.NewEncoder(w).Encode(ollamaResponse{Embeddings: out})
	}))
	defer srv.Close()
	p, err := NewOllama(srv.URL, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if p.Dimension() != 0 || p.Name() != "ollama:"+ollamaDefaultModel {
		t.Fatalf("before the first embedding: dim=%d name=%q", p.Dimension(), p.Name())
	}
	vecs, err := p.Embed(context.Background(), []string{"a", "b"})
	if err != nil || len(vecs) != 2 || len(vecs[1]) != 4 || gotModel != ollamaDefaultModel {
		t.Fatalf("embed: %v %v model=%q", err, vecs, gotModel)
	}
	if p.Dimension() != 4 {
		t.Fatalf("dimension learned from the first embedding: %d", p.Dimension())
	}
	down, _ := NewOllama("http://127.0.0.1:1", "x", 0)
	if _, err := down.Embed(context.Background(), []string{"a"}); err == nil {
		t.Fatal("an unreachable server must fail on embed")
	}
	if p2, err := NewOllama("localhost:11434", "m", 8); err != nil || p2.host != "http://localhost:11434" || p2.Dimension() != 8 {
		t.Fatalf("scheme default and explicit dim: %v %+v", err, p2)
	}
	if _, err := NewByName("ollama"); err == nil {
		// Only passes when a local Ollama happens to run; either outcome is fine.
		t.Log("local ollama reachable")
	}
}

func TestInstrument_ReportsUsageAndPrices(t *testing.T) {
	var seen []string
	var chars int
	SetUsageObserver(func(provider string, texts, c int, err error) {
		seen = append(seen, provider)
		chars += c
	})
	t.Cleanup(func() { SetUsageObserver(nil) })
	p := Instrument(&fakeProvider{name: "openai:text-embedding-3-small"})
	if Instrument(p) != p {
		t.Fatal("instrumenting twice must be a no-op")
	}
	if _, err := p.Embed(context.Background(), []string{"hello", "world!"}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] != "openai:text-embedding-3-small" || chars != 11 {
		t.Fatalf("observer: %v %d", seen, chars)
	}
	if Instrument(NewNull()) != nil && !IsNull(Instrument(NewNull())) {
		t.Fatal("null stays null")
	}
	for name, want := range map[string]float64{
		"openai:text-embedding-3-small": 0.02, "openai:text-embedding-3-large": 0.13, "voyage:voyage-4": 0.06,
		"google:gemini-embedding-2": 0.15, "bedrock:amazon.titan-embed-text-v2:0": 0.02, "ollama:nomic-embed-text": 0, "fake:x": 0,
	} {
		if got := PricePerMillionTokens(name); got != want {
			t.Errorf("price(%s) = %v, want %v", name, got, want)
		}
	}
}

type fakeProvider struct{ name string }

func (f *fakeProvider) Name() string   { return f.name }
func (f *fakeProvider) Dimension() int { return 2 }
func (f *fakeProvider) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, errors.New("empty")
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{1, 0}
	}
	return out, nil
}
