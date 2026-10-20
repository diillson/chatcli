/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type windowFakeSessionAdapter struct {
	fakeSessionAdapter
	query, from, to, asOf string
	limit                 int
}

func (f *windowFakeSessionAdapter) SearchWindow(_ context.Context, query string, limit int, from, to, asOf string) (string, error) {
	f.query, f.limit, f.from, f.to, f.asOf = query, limit, from, to, asOf
	return "window-ok", nil
}

func TestSession_SearchDateFlags(t *testing.T) {
	ctx := context.Background()
	p := NewBuiltinSessionPlugin()

	w := &windowFakeSessionAdapter{}
	withSessionAdapter(t, w)
	out, err := p.Execute(ctx, []string{`{"cmd":"search","args":{"query":"ibotta","to":"3 weeks ago","as_of":"2023-05-30"}}`})
	if err != nil || out != "window-ok" || w.query != "ibotta" || w.to != "3 weeks ago" || w.asOf != "2023-05-30" || w.limit != 3 {
		t.Fatalf("dispatch: out=%q err=%v got %+v", out, err, w)
	}
	// A window alone lists the window's sessions.
	if _, err := p.Execute(ctx, []string{`{"cmd":"search","args":{"from":"2023-05"}}`}); err != nil || w.from != "2023-05" || w.query != "" {
		t.Fatalf("window-only search: err=%v got %+v", err, w)
	}
	if _, err := p.Execute(ctx, []string{`{"cmd":"search","args":{}}`}); err == nil {
		t.Fatal("no query and no window must be an error")
	}

	// An adapter without the capability refuses date flags instead of
	// silently searching the whole store.
	plain := &fakeSessionAdapter{out: "plain"}
	withSessionAdapter(t, plain)
	if _, err := p.Execute(ctx, []string{`{"cmd":"search","args":{"query":"x","from":"2023-05"}}`}); err == nil {
		t.Fatal("date flags without the capability must be an error")
	}
	if out, err := p.Execute(ctx, []string{`{"cmd":"search","args":{"query":"x"}}`}); err != nil || out != "plain" {
		t.Fatalf("plain search: %q %v", out, err)
	}
}

type anchoredFakeMem struct {
	timelineFakeAdapter
	recallQ, asOf string
}

func (f *anchoredFakeMem) RecallAt(q, asOf string) (string, error) {
	f.recallQ, f.asOf = q, asOf
	return "recall-at", nil
}

func (f *anchoredFakeMem) TimelineAt(project, from, to, query string, limit int, asOf string) (string, error) {
	f.project, f.from, f.to, f.query, f.limit, f.asOf = project, from, to, query, limit, asOf
	return "timeline-at", nil
}

func TestMemory_AsOfDispatch(t *testing.T) {
	ctx := context.Background()
	p := NewBuiltinMemoryPlugin()
	f := &anchoredFakeMem{}
	SetMemoryAdapter(f)
	t.Cleanup(func() { SetMemoryAdapter(nil) })

	if out, err := p.Execute(ctx, []string{`{"cmd":"recall","args":{"query":"há 3 meses","as_of":"2023-05-30"}}`}); err != nil || out != "recall-at" || f.asOf != "2023-05-30" {
		t.Fatalf("recall as_of: %q %v %+v", out, err, f)
	}
	if out, err := p.Execute(ctx, []string{`{"cmd":"timeline","args":{"from":"2 weeks ago","as_of":"2023-05-30","limit":5}}`}); err != nil || out != "timeline-at" || f.from != "2 weeks ago" || f.limit != 5 {
		t.Fatalf("timeline as_of: %q %v %+v", out, err, f)
	}
	// Without as_of the plain paths run.
	if out, _ := p.Execute(ctx, []string{`{"cmd":"timeline","args":{"query":"x"}}`}); out != "timeline-ok" {
		t.Fatalf("plain timeline: %q", out)
	}

	SetMemoryAdapter(&timelineFakeAdapter{})
	if _, err := p.Execute(ctx, []string{`{"cmd":"recall","args":{"query":"x","as_of":"2023-05-30"}}`}); err == nil {
		t.Fatal("as_of without the capability must be an error")
	}
	if _, err := p.Execute(ctx, []string{`{"cmd":"timeline","args":{"query":"x","as_of":"2023-05-30"}}`}); err == nil {
		t.Fatal("timeline as_of without the capability must be an error")
	}
}

// The model reads the schema: every new flag must be listed there.
func TestRecallSchemas_ListTheDateFlags(t *testing.T) {
	for _, s := range []string{NewBuiltinSessionPlugin().Schema(), NewBuiltinMemoryPlugin().Schema()} {
		if !json.Valid([]byte(s)) || !strings.Contains(s, `"as_of"`) {
			t.Fatalf("schema lacks as_of: %s", s[:200])
		}
	}
	if s := NewBuiltinSessionPlugin().Schema(); !strings.Contains(s, `"from"`) || !strings.Contains(s, `"to"`) {
		t.Fatal("session schema lacks from/to")
	}
}
