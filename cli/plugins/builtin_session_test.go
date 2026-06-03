/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"context"
	"testing"
)

type fakeSessionAdapter struct {
	lastQuery string
	lastLimit int
	out       string
}

func (f *fakeSessionAdapter) Search(_ context.Context, query string, limit int) (string, error) {
	f.lastQuery = query
	f.lastLimit = limit
	return f.out, nil
}
func (f *fakeSessionAdapter) List(context.Context) (string, error) { return "sessions", nil }

func withSessionAdapter(t *testing.T, a SessionAdapter) {
	t.Helper()
	SetSessionAdapter(a)
	t.Cleanup(func() { SetSessionAdapter(nil) })
}

func TestSession_NoAdapter(t *testing.T) {
	SetSessionAdapter(nil)
	p := NewBuiltinSessionPlugin()
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"list"}`}); err == nil {
		t.Fatal("expected error with no adapter")
	}
}

func TestSession_EnvelopeSearch(t *testing.T) {
	f := &fakeSessionAdapter{out: "hits"}
	withSessionAdapter(t, f)
	p := NewBuiltinSessionPlugin()
	out, err := p.Execute(context.Background(), []string{`{"cmd":"search","args":{"query":"cache design","limit":5}}`})
	if err != nil {
		t.Fatal(err)
	}
	if out != "hits" || f.lastQuery != "cache design" || f.lastLimit != 5 {
		t.Fatalf("got out=%q query=%q limit=%d", out, f.lastQuery, f.lastLimit)
	}
}

func TestSession_ArgvSearchDefaultLimit(t *testing.T) {
	f := &fakeSessionAdapter{}
	withSessionAdapter(t, f)
	p := NewBuiltinSessionPlugin()
	if _, err := p.Execute(context.Background(), []string{"search", "auth", "refactor"}); err != nil {
		t.Fatal(err)
	}
	if f.lastQuery != "auth refactor" || f.lastLimit != 3 {
		t.Fatalf("query=%q limit=%d", f.lastQuery, f.lastLimit)
	}
}

func TestSession_MissingQuery(t *testing.T) {
	withSessionAdapter(t, &fakeSessionAdapter{})
	p := NewBuiltinSessionPlugin()
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"search","args":{}}`}); err == nil {
		t.Fatal("expected error for missing query")
	}
}

func TestSession_List(t *testing.T) {
	withSessionAdapter(t, &fakeSessionAdapter{})
	p := NewBuiltinSessionPlugin()
	out, err := p.Execute(context.Background(), []string{`{"cmd":"list"}`})
	if err != nil || out != "sessions" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestCanonicalSessionCmd(t *testing.T) {
	for _, in := range []string{"search", "find", "recall"} {
		if canonicalSessionCmd(in) != "search" {
			t.Errorf("%q != search", in)
		}
	}
	if canonicalSessionCmd("list") != "list" || canonicalSessionCmd("zz") != "" {
		t.Fatal("canonicalSessionCmd mismatch")
	}
}

func TestSession_FlattenedArgvFromAgent(t *testing.T) {
	f := &fakeSessionAdapter{out: "hits"}
	withSessionAdapter(t, f)
	p := NewBuiltinSessionPlugin()
	if _, err := p.Execute(context.Background(), []string{"search", "--query", "rate limiter design", "--limit", "5"}); err != nil {
		t.Fatal(err)
	}
	if f.lastQuery != "rate limiter design" || f.lastLimit != 5 {
		t.Fatalf("query=%q limit=%d", f.lastQuery, f.lastLimit)
	}
}
