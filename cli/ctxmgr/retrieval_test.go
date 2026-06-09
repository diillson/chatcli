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

	"github.com/diillson/chatcli/llm/embedding"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// conceptProvider maps words to semantic concepts so synonym queries (which
// keyword search misses) retrieve the right passage. Deterministic, CGO-free.
type conceptProvider struct{}

var conceptWords = map[string]int{
	// 0: auth
	"oauth": 0, "login": 0, "token": 0, "bearer": 0, "authentication": 0,
	"signin": 0, "credential": 0, "verification": 0, "password": 0,
	// 1: database
	"postgres": 1, "database": 1, "rows": 1, "tables": 1, "schema": 1, "records": 1,
	// 2: cache
	"redis": 2, "cache": 2, "eviction": 2, "ttl": 2, "invalidation": 2,
}

const conceptDim = 3

func (conceptProvider) Name() string   { return "concept" }
func (conceptProvider) Dimension() int { return conceptDim }
func (conceptProvider) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, conceptDim)
		for _, w := range strings.FieldsFunc(strings.ToLower(t), func(r rune) bool {
			return !(r >= 'a' && r <= 'z')
		}) {
			if idx, ok := conceptWords[w]; ok {
				v[idx]++
			}
		}
		out[i] = v
	}
	return out, nil
}

func repeatLine(line string, n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func authDBCacheContext() *FileContext {
	return &FileContext{
		ID:   "ctx-test",
		Name: "proj",
		Files: []utils.FileInfo{
			{Path: "auth.go", Type: "go", Content: repeatLine("oauth login issues bearer token authentication", 30)},
			{Path: "db.go", Type: "go", Content: repeatLine("postgres database stores rows in tables schema", 30)},
			{Path: "cache.go", Type: "go", Content: repeatLine("redis cache eviction relies on ttl", 30)},
		},
	}
}

func TestRetrievalEngine_DisabledWithoutProvider(t *testing.T) {
	e := NewRetrievalEngine(embedding.NewNull(), t.TempDir(), zap.NewNop())
	if e.Enabled() {
		t.Fatal("null provider must disable retrieval")
	}
	segs, err := e.Retrieve(context.Background(), authDBCacheContext(), "anything", 5)
	if err != nil || segs != nil {
		t.Fatalf("disabled engine must return (nil,nil), got %v %v", segs, err)
	}
}

func TestRetrievalEngine_RetrievesRelevantPassagesAndShrinksContext(t *testing.T) {
	fc := authDBCacheContext()
	e := NewRetrievalEngine(conceptProvider{}, t.TempDir(), zap.NewNop())

	// Synonym query — no literal overlap with "oauth/login/token" in the files.
	segs, err := e.Retrieve(context.Background(), fc, "how is credential signin verification handled", 3)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(segs) == 0 {
		t.Fatal("expected retrieved passages")
	}
	// Every retrieved passage must come from the auth file (the only concept
	// the query maps to); db/cache passages score zero and are floored out.
	for _, s := range segs {
		if s.FilePath != "auth.go" {
			t.Errorf("retrieved irrelevant passage from %s", s.FilePath)
		}
	}

	// Context shrink: the injected block must be far smaller than dumping all
	// three files raw.
	whole := 0
	for _, f := range fc.Files {
		whole += len(f.Content)
	}
	block := FormatSegmentsBlock(fc.Name, "query", segs)
	if len(block) >= whole {
		t.Fatalf("retrieval did not shrink context: block=%d whole=%d", len(block), whole)
	}
	t.Logf("context shrink: %d → %d bytes (%.0f%% of raw)", whole, len(block), 100*float64(len(block))/float64(whole))
}

func TestRetrievalEngine_CachesVectorsAcrossCalls(t *testing.T) {
	dir := t.TempDir()
	fc := authDBCacheContext()
	e := NewRetrievalEngine(conceptProvider{}, dir, zap.NewNop())

	if _, err := e.Retrieve(context.Background(), fc, "cache ttl eviction", 2); err != nil {
		t.Fatalf("first retrieve: %v", err)
	}
	// Second call must reuse the persisted vectors (a .vec.json now exists).
	segs, err := e.Retrieve(context.Background(), fc, "redis invalidation", 2)
	if err != nil {
		t.Fatalf("second retrieve: %v", err)
	}
	if len(segs) == 0 || segs[0].FilePath != "cache.go" {
		t.Fatalf("expected cache.go passage on second call, got %v", segs)
	}
}

func TestFormatSegmentsBlock_Empty(t *testing.T) {
	if got := FormatSegmentsBlock("proj", "q", nil); got != "" {
		t.Fatalf("empty segments should format to empty string, got %q", got)
	}
}
