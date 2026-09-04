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
	"time"
	"unicode/utf8"

	"github.com/diillson/chatcli/utils"
)

func TestSegmentOne_SkipsBinaryAndMinifiedCapsLongLines(t *testing.T) {
	opts := SegmentOptions{}.sanitized()
	if segs := segmentOne(utils.FileInfo{Path: "a.bin", Content: "abc\x00def", Type: "Binary"}, opts); len(segs) != 0 {
		t.Fatal("NUL byte content must not be segmented")
	}
	if segs := segmentOne(utils.FileInfo{Path: "b.txt", Content: "ok\xff\xfe"}, opts); len(segs) != 0 {
		t.Fatal("invalid UTF-8 must not be segmented")
	}
	minified := strings.Repeat("function a(){return 1};", 2000) + "\n" + strings.Repeat("var b=2;", 3000)
	if segs := segmentOne(utils.FileInfo{Path: "bundle.min.js", Content: minified, Type: "JavaScript"}, opts); len(segs) != 0 {
		t.Fatalf("minified bundle must be skipped, got %d passages", len(segs))
	}
	// Ordinary source with one very long line: the line is cut rune-safe
	// at the hard cap and the rest of the file still segments.
	long := strings.Repeat("é", opts.MaxChars*passageHardCapFactor) // 2 bytes each → over the cap
	src := "line one\n" + long + "\nline three\n"
	segs := segmentOne(utils.FileInfo{Path: "c.go", Content: src, Type: "Go"}, opts)
	if len(segs) == 0 {
		t.Fatal("ordinary text must segment")
	}
	for _, s := range segs {
		if !utf8.ValidString(s.Content) {
			t.Fatal("passage cut split a rune")
		}
		for _, l := range strings.Split(s.Content, "\n") {
			if len(l) > opts.MaxChars*passageHardCapFactor+len("…") {
				t.Fatalf("line over the hard cap: %d bytes", len(l))
			}
		}
	}
	if unembeddableReason("just a normal\nshort file\n") != "" || unembeddableReason(strings.Repeat("a normal line of code\n", 500)) != "" {
		t.Fatal("ordinary text is embeddable")
	}
}

// flakyProvider fails the embedding request that contains a poisoned text.
type flakyProvider struct {
	poison string
	calls  int
	texts  int
}

func (p *flakyProvider) Name() string   { return "fake:flaky" }
func (p *flakyProvider) Dimension() int { return 3 }
func (p *flakyProvider) Embed(_ context.Context, texts []string) ([][]float32, error) {
	p.calls++
	for _, t := range texts {
		if strings.Contains(t, p.poison) {
			return nil, errors.New("400 invalid input")
		}
	}
	p.texts += len(texts)
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{1, 0, float32(i)}
	}
	return out, nil
}

func bigContext(n int, poisonAt int) *FileContext {
	fc := &FileContext{ID: "big", Name: "big", UpdatedAt: time.Now(), Mode: ModeKnowledge}
	for i := 0; i < n; i++ {
		body := strings.Repeat("passage text ", 20)
		if i == poisonAt {
			body = "POISON " + body
		}
		fc.Files = append(fc.Files, utils.FileInfo{Path: "f" + itoa(i) + ".md", Content: body, Size: int64(len(body)), Type: "Markdown"})
	}
	fc.FileCount = len(fc.Files)
	return fc
}

func TestWarm_SkipsAFailingBatchAndContinues(t *testing.T) {
	p := &flakyProvider{poison: "POISON"}
	e := NewRetrievalEngine(p, t.TempDir(), nil)
	fc := bigContext(200, 10) // > 3 batches of 64; the poison sits in the first
	n, err := e.Warm(context.Background(), fc)
	if err == nil {
		t.Fatal("the failing batch must be reported")
	}
	if n < 200-warmBatch || n >= 200 {
		t.Fatalf("the other batches must still land: embedded=%d", n)
	}
	// A retrieval over the partially warmed context still answers, and
	// does not re-send what is already embedded.
	before := p.texts
	hits, err := e.Retrieve(context.Background(), fc, "passage text", 3)
	if err != nil || len(hits) == 0 {
		t.Fatalf("retrieve after a partial warm: hits=%d err=%v", len(hits), err)
	}
	if p.texts-before > warmBatch {
		t.Fatalf("retrieve re-embedded already-stored passages: %d", p.texts-before)
	}
}

func TestRetrieve_EmbedsInBoundedBatchesUnderSingleFlight(t *testing.T) {
	p := &warmProvider{}
	e := NewRetrievalEngine(p, t.TempDir(), nil)
	fc := bigContext(150, -1)
	if _, err := e.Retrieve(context.Background(), fc, "passage text", 3); err != nil {
		t.Fatal(err)
	}
	// 150 passages + 1 query: at least 3 passage requests, none above the batch.
	if p.calls < 4 {
		t.Fatalf("retrieve must batch its embeddings: calls=%d", p.calls)
	}
	// A poisoned context where every batch fails is a real error.
	all := &flakyProvider{poison: "passage"}
	e2 := NewRetrievalEngine(all, t.TempDir(), nil)
	if _, err := e2.Retrieve(context.Background(), bigContext(5, -1), "passage text", 3); err == nil {
		t.Fatal("nothing embedded must surface the provider error")
	}
}

func TestFitSegments_RuneSafeCutAdjustsTheCitation(t *testing.T) {
	seg := Segment{FilePath: "x.md", StartLine: 10, EndLine: 40, Content: strings.Repeat("ünïcödé line\n", 60)}
	out := fitSegments([]Segment{seg}, 400)
	if len(out) != 1 {
		t.Fatalf("first passage must fit truncated: %d", len(out))
	}
	if !utf8.ValidString(out[0].Content) {
		t.Fatal("cut split a rune")
	}
	if out[0].EndLine >= 40 || out[0].EndLine < 10 {
		t.Fatalf("EndLine must follow the kept lines: %d", out[0].EndLine)
	}
	if want := 10 + strings.Count(out[0].Content, "\n"); out[0].EndLine != want {
		t.Fatalf("EndLine=%d want %d", out[0].EndLine, want)
	}
}
