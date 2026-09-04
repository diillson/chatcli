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

// keywordProvider embeds by concept: texts mentioning a horse-like animal
// share one direction, everything else another — so a passage can be
// semantically close to a query it shares no word with.
type keywordProvider struct{ queryEmbeds int }

func (p *keywordProvider) Name() string   { return "fake:keyword" }
func (p *keywordProvider) Dimension() int { return 3 }
func (p *keywordProvider) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 1 {
		p.queryEmbeds++
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		l := strings.ToLower(t)
		switch {
		case strings.Contains(l, "zebra") || strings.Contains(l, "equine"):
			out[i] = []float32{1, 0, 0}
		default:
			out[i] = []float32{0, 1, 0}
		}
	}
	return out, nil
}

func hybridContext() *FileContext {
	fc := &FileContext{ID: "h1", Name: "h1", UpdatedAt: time.Now(), Mode: ModeKnowledge}
	files := []utils.FileInfo{
		{Path: "install.md", Content: "Installation guide: run the installer, then restart the installation service.\n", Type: "Markdown"},
		{Path: "config.md", Content: "Installation options and configuration keys for the installer.\n", Type: "Markdown"},
		{Path: "zebra.md", Content: "The zebra module handles striped rendering of charts.\n", Type: "Markdown"},
	}
	for i := range files {
		files[i].Size = int64(len(files[i].Content))
	}
	fc.Files = files
	fc.FileCount = len(files)
	return fc
}

func TestRetrieveHybrid_UnionBringsVectorOnlyHits(t *testing.T) {
	p := &keywordProvider{}
	e := NewRetrievalEngine(p, t.TempDir(), nil)
	fc := hybridContext()
	if _, err := e.Warm(context.Background(), fc); err != nil {
		t.Fatal(err)
	}
	// "installation" hits BM25 on two files; "equine" hits nothing lexically
	// but the zebra passage is its nearest vector — the union must surface it.
	scored, err := e.RetrieveHybridScored(context.Background(), fc, "equine installation", 3)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range scored {
		if s.Segment.FilePath == "zebra.md" {
			found = true
		}
	}
	if !found {
		t.Fatalf("vector-only hit must join the BM25 pool: %+v", scored)
	}
	if len(scored) < 2 || scored[0].Score <= scored[len(scored)-1].Score {
		t.Fatalf("RRF order must be descending: %+v", scored)
	}
}

func TestRetrieveHybrid_EmbedsTheQueryOncePerTurn(t *testing.T) {
	p := &keywordProvider{}
	e := NewRetrievalEngine(p, t.TempDir(), nil)
	fc := hybridContext()
	_, _ = e.Warm(context.Background(), fc)
	p.queryEmbeds = 0
	for i := 0; i < 3; i++ {
		if _, err := e.RetrieveHybridScored(context.Background(), fc, "installation service", 3); err != nil {
			t.Fatal(err)
		}
	}
	if p.queryEmbeds != 1 {
		t.Fatalf("the same query must be embedded once, got %d", p.queryEmbeds)
	}
	if _, err := e.RetrieveHybridScored(context.Background(), fc, "another question", 3); err != nil {
		t.Fatal(err)
	}
	if p.queryEmbeds != 2 {
		t.Fatalf("a new query embeds once more: %d", p.queryEmbeds)
	}
}
