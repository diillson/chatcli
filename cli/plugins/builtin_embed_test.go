/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diillson/chatcli/llm/embedding"
)

// fakeEmbedProvider maps known texts to fixed vectors so similarity and
// ranking are deterministic.
type fakeEmbedProvider struct {
	vectors map[string][]float32
}

func (f *fakeEmbedProvider) Name() string   { return "fake:test-model" }
func (f *fakeEmbedProvider) Dimension() int { return 2 }
func (f *fakeEmbedProvider) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v, ok := f.vectors[t]
		if !ok {
			v = []float32{0, 1}
		}
		out[i] = v
	}
	return out, nil
}

func withFakeEmbedProvider(t *testing.T, p embedding.Provider) {
	t.Helper()
	prev := embedProviderFactory
	embedProviderFactory = func() (embedding.Provider, error) { return p, nil }
	t.Cleanup(func() { embedProviderFactory = prev })
}

func TestParseEmbedInvocation(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantCmd string
	}{
		{"envelope", []string{`{"cmd":"similarity","args":{"a":"x","b":"y"}}`}, "similarity"},
		{"alias", []string{`{"cmd":"compare","args":{"a":"x","b":"y"}}`}, "similarity"},
		{"flat similarity", []string{`{"a":"x","b":"y"}`}, "similarity"},
		{"flat rank", []string{`{"query":"q","candidates":["c1"]}`}, "rank"},
		{"flat text", []string{`{"texts":["t1"]}`}, "text"},
		{"argv", []string{"status"}, "status"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd, _, err := parseEmbedInvocation(tc.args)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if cmd != tc.wantCmd {
				t.Errorf("cmd = %q, want %q", cmd, tc.wantCmd)
			}
		})
	}
	if _, _, err := parseEmbedInvocation([]string{`{"cmd":"bogus","args":{}}`}); err == nil {
		t.Error("expected error for unknown cmd with no inferable args")
	}
}

func TestEmbedStatus_NullProvider(t *testing.T) {
	withFakeEmbedProvider(t, embedding.NewNull())
	p := NewBuiltinEmbedPlugin()
	out, err := p.Execute(context.Background(), []string{`{"cmd":"status"}`})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out, "no embedding backend configured") {
		t.Errorf("status output should hint at configuration; got %q", out)
	}
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"similarity","args":{"a":"x","b":"y"}}`}); err == nil {
		t.Error("similarity must error when no backend is configured")
	}
}

func TestEmbedSimilarityAndRank(t *testing.T) {
	withFakeEmbedProvider(t, &fakeEmbedProvider{vectors: map[string][]float32{
		"query":    {1, 0},
		"close":    {1, 0.1},
		"far":      {0, 1},
		"opposite": {-1, 0},
	}})
	p := NewBuiltinEmbedPlugin()

	out, err := p.Execute(context.Background(), []string{`{"cmd":"similarity","args":{"a":"query","b":"close"}}`})
	if err != nil {
		t.Fatalf("similarity: %v", err)
	}
	if !strings.Contains(out, "0.99") {
		t.Errorf("expected near-1 similarity; got %q", out)
	}

	out, err = p.Execute(context.Background(), []string{`{"cmd":"rank","args":{"query":"query","candidates":["far","close","opposite"],"k":2}}`})
	if err != nil {
		t.Fatalf("rank: %v", err)
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 3 {
		t.Fatalf("expected header + 2 ranked lines; got %q", out)
	}
	if !strings.Contains(lines[1], "close") || !strings.Contains(lines[2], "far") {
		t.Errorf("ranking order wrong: %q", out)
	}
}

func TestEmbedText_WritesJSONL(t *testing.T) {
	withFakeEmbedProvider(t, &fakeEmbedProvider{vectors: map[string][]float32{"a": {1, 0}}})
	p := NewBuiltinEmbedPlugin()
	out := filepath.Join(t.TempDir(), "sub", "vectors.jsonl")
	res, err := p.Execute(context.Background(), []string{`{"cmd":"text","args":{"texts":["a","b"],"out":"` + out + `"}}`})
	if err != nil {
		t.Fatalf("text: %v", err)
	}
	if !strings.Contains(res, out) {
		t.Errorf("result should report the output path; got %q", res)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatalf("open output: %v", err)
	}
	defer func() { _ = f.Close() }()
	var count int
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var row struct {
			Index     int       `json:"index"`
			Text      string    `json:"text"`
			Embedding []float32 `json:"embedding"`
		}
		if err := json.Unmarshal(sc.Bytes(), &row); err != nil {
			t.Fatalf("line %d: %v", count, err)
		}
		if len(row.Embedding) != 2 {
			t.Errorf("line %d: dim %d, want 2", count, len(row.Embedding))
		}
		count++
	}
	if count != 2 {
		t.Errorf("expected 2 JSONL rows; got %d", count)
	}

	// Without "out", only a summary is returned — nothing written.
	res, err = p.Execute(context.Background(), []string{`{"cmd":"text","args":{"text":"a"}}`})
	if err != nil {
		t.Fatalf("text summary: %v", err)
	}
	if !strings.Contains(res, "dimension 2") {
		t.Errorf("summary should report the dimension; got %q", res)
	}
}

func TestEmbedCaps(t *testing.T) {
	p := NewBuiltinEmbedPlugin()
	if !p.IsReadOnly([]string{`{"cmd":"similarity","args":{"a":"x","b":"y"}}`}) {
		t.Error("similarity must be read-only")
	}
	if p.IsReadOnly([]string{`{"cmd":"text","args":{"text":"a","out":"/tmp/v.jsonl"}}`}) {
		t.Error("text with out writes a file — not read-only")
	}
	if !p.IsReadOnly([]string{`{"cmd":"text","args":{"text":"a"}}`}) {
		t.Error("text without out must be read-only")
	}
	if !p.IsConcurrencySafe(nil) {
		t.Error("@embed must be concurrency-safe")
	}
	if d := p.DescribeCall([]string{`{"cmd":"rank","args":{"query":"billing","candidates":["x"]}}`}); !strings.Contains(d, "billing") {
		t.Errorf("rank describe should include the query; got %q", d)
	}
}

func TestEmbedErrors(t *testing.T) {
	withFakeEmbedProvider(t, &fakeEmbedProvider{})
	p := NewBuiltinEmbedPlugin()
	for name, args := range map[string][]string{
		"empty":                {},
		"similarity missing b": {`{"cmd":"similarity","args":{"a":"x"}}`},
		"rank no candidates":   {`{"cmd":"rank","args":{"query":"q"}}`},
		"text no input":        {`{"cmd":"text","args":{}}`},
	} {
		if _, err := p.Execute(context.Background(), args); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
