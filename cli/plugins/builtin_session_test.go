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
	if canonicalSessionCmd("bind") != "attach" || canonicalSessionCmd("unbind") != "detach" || canonicalSessionCmd("branch") != "fork" {
		t.Fatal("write-op aliases mismatch")
	}
	// Destructive spellings must stay unmapped (non-destructive surface).
	if canonicalSessionCmd("delete") != "" || canonicalSessionCmd("load") != "" {
		t.Fatal("delete/load must not canonicalize to any op")
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

// fakeSessionWriterAdapter layers the optional SessionWriter capability on
// top of the read-only fake, mirroring how the real adapter composes.
type fakeSessionWriterAdapter struct {
	fakeSessionAdapter
	saved    string
	forked   string
	attached string
	detached bool
}

func (f *fakeSessionWriterAdapter) Save(_ context.Context, name string) (string, error) {
	f.saved = name
	return "saved", nil
}
func (f *fakeSessionWriterAdapter) Fork(_ context.Context, name string) (string, error) {
	f.forked = name
	return "forked", nil
}
func (f *fakeSessionWriterAdapter) Attach(_ context.Context, name string) (string, error) {
	f.attached = name
	return "attached", nil
}
func (f *fakeSessionWriterAdapter) Detach(context.Context) (string, error) {
	f.detached = true
	return "detached", nil
}

func TestSession_WriteOpsRequireWriterCapability(t *testing.T) {
	// The read-only fake does NOT implement SessionWriter — every write op
	// must fail with a capability error instead of panicking or no-opping.
	withSessionAdapter(t, &fakeSessionAdapter{})
	p := NewBuiltinSessionPlugin()
	for _, cmd := range []string{"save", "fork", "attach", "detach"} {
		if _, err := p.Execute(context.Background(), []string{`{"cmd":"` + cmd + `","args":{"name":"x"}}`}); err == nil {
			t.Fatalf("%s: expected error without SessionWriter", cmd)
		}
	}
}

func TestSession_WriteOpsDispatch(t *testing.T) {
	f := &fakeSessionWriterAdapter{}
	withSessionAdapter(t, f)
	p := NewBuiltinSessionPlugin()

	if out, err := p.Execute(context.Background(), []string{`{"cmd":"save","args":{"name":"auth-refactor"}}`}); err != nil || out != "saved" {
		t.Fatalf("save: out=%q err=%v", out, err)
	}
	if f.saved != "auth-refactor" {
		t.Fatalf("save name = %q", f.saved)
	}
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"fork","args":{"name":"auth-v2"}}`}); err != nil {
		t.Fatal(err)
	}
	if f.forked != "auth-v2" {
		t.Fatalf("fork name = %q", f.forked)
	}
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"attach","args":{"name":"proj"}}`}); err != nil {
		t.Fatal(err)
	}
	if f.attached != "proj" {
		t.Fatalf("attach name = %q", f.attached)
	}
	if out, err := p.Execute(context.Background(), []string{`{"cmd":"detach"}`}); err != nil || out != "detached" {
		t.Fatalf("detach: out=%q err=%v", out, err)
	}
	if !f.detached {
		t.Fatal("detach not dispatched")
	}
}

func TestSession_WriteOpsArgvPositionalIsName(t *testing.T) {
	// Agent argv flattening: `save myname` must map the positional to "name"
	// (the read ops keep mapping it to "query").
	f := &fakeSessionWriterAdapter{}
	withSessionAdapter(t, f)
	p := NewBuiltinSessionPlugin()
	if _, err := p.Execute(context.Background(), []string{"save", "myname"}); err != nil {
		t.Fatal(err)
	}
	if f.saved != "myname" {
		t.Fatalf("positional save name = %q", f.saved)
	}
	if _, err := p.Execute(context.Background(), []string{"attach", "--name", "other"}); err != nil {
		t.Fatal(err)
	}
	if f.attached != "other" {
		t.Fatalf("flag attach name = %q", f.attached)
	}
}

func TestSession_WriteOpsRequireName(t *testing.T) {
	withSessionAdapter(t, &fakeSessionWriterAdapter{})
	p := NewBuiltinSessionPlugin()
	for _, cmd := range []string{"save", "fork", "attach"} {
		if _, err := p.Execute(context.Background(), []string{`{"cmd":"` + cmd + `","args":{}}`}); err == nil {
			t.Fatalf("%s without name must error", cmd)
		}
	}
}

func TestSession_DeleteIsNotAnOp(t *testing.T) {
	// Product decision: the model never deletes sessions. "delete" (and
	// "load") must stay unknown even with a writer-capable adapter wired.
	withSessionAdapter(t, &fakeSessionWriterAdapter{})
	p := NewBuiltinSessionPlugin()
	for _, cmd := range []string{"delete", "load"} {
		if _, err := p.Execute(context.Background(), []string{`{"cmd":"` + cmd + `","args":{"name":"x"}}`}); err == nil {
			t.Fatalf("%s must be rejected as unknown", cmd)
		}
	}
}

func TestSession_Capabilities(t *testing.T) {
	p := NewBuiltinSessionPlugin()
	for _, cmd := range []string{"search", "get", "list"} {
		args := []string{`{"cmd":"` + cmd + `","args":{"query":"x","name":"n"}}`}
		if !p.IsReadOnly(args) || !p.IsConcurrencySafe(args) {
			t.Fatalf("%s must be read-only + concurrency-safe", cmd)
		}
	}
	for _, cmd := range []string{"save", "fork", "attach", "detach"} {
		args := []string{`{"cmd":"` + cmd + `","args":{"name":"n"}}`}
		if p.IsReadOnly(args) || p.IsConcurrencySafe(args) {
			t.Fatalf("%s must NOT be read-only nor concurrency-safe", cmd)
		}
	}
	// Unparseable input fails closed.
	if p.IsReadOnly(nil) || p.IsConcurrencySafe(nil) {
		t.Fatal("empty args must fail closed")
	}
}

func TestSession_FlatNativeArgs(t *testing.T) {
	f := &fakeSessionAdapter{out: "ok"}
	withSessionAdapter(t, f)
	p := NewBuiltinSessionPlugin()
	// native tool calling may send a flat object with no "cmd"
	if _, err := p.Execute(context.Background(), []string{`{"query":"rate limiter"}`}); err != nil {
		t.Fatal(err)
	}
	if f.lastQuery != "rate limiter" {
		t.Fatalf("flat native query = %q", f.lastQuery)
	}
}
