/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

// fakeFactBrowser implements MemoryAdapter plus FactBrowser.
type fakeFactBrowser struct {
	calls []string
}

func (f *fakeFactBrowser) Remember(content, category string) (string, error) {
	f.calls = append(f.calls, "remember")
	return "ok", nil
}
func (f *fakeFactBrowser) UpdateProfile(map[string]string) (string, error) { return "ok", nil }
func (f *fakeFactBrowser) Forget(match string) (string, error) {
	f.calls = append(f.calls, "forget:"+match)
	return "removed", nil
}
func (f *fakeFactBrowser) Recall(string) (string, error) { return "recall", nil }
func (f *fakeFactBrowser) ListFacts(match, category string, limit int) (string, error) {
	f.calls = append(f.calls, "list:"+match+":"+category+":"+strconv.Itoa(limit))
	return "facts", nil
}
func (f *fakeFactBrowser) ForgetPreview(match string) (string, error) {
	f.calls = append(f.calls, "preview:"+match)
	return "would remove", nil
}
func (f *fakeFactBrowser) ForgetByID(id string) (string, error) {
	f.calls = append(f.calls, "byid:"+id)
	return "removed one", nil
}

func TestMemoryListAndForgetForms(t *testing.T) {
	fake := &fakeFactBrowser{}
	SetMemoryAdapter(fake)
	t.Cleanup(func() { SetMemoryAdapter(nil) })
	p := NewBuiltinMemoryPlugin()
	ctx := context.Background()

	if out, err := p.Execute(ctx, []string{`{"cmd":"list","args":{"match":"tabs","category":"preference","limit":5}}`}); err != nil || out != "facts" || fake.calls[len(fake.calls)-1] != "list:tabs:preference:5" {
		t.Fatalf("list: %q %v %v", out, err, fake.calls)
	}
	if _, err := p.Execute(ctx, []string{"facts"}); err != nil || !strings.HasPrefix(fake.calls[len(fake.calls)-1], "list:") {
		t.Fatalf("list alias: %v %v", err, fake.calls)
	}
	if out, err := p.Execute(ctx, []string{`{"cmd":"forget","args":{"match":"tabs","dry_run":true}}`}); err != nil || out != "would remove" || fake.calls[len(fake.calls)-1] != "preview:tabs" {
		t.Fatalf("dry run: %q %v %v", out, err, fake.calls)
	}
	if out, err := p.Execute(ctx, []string{`{"cmd":"forget","args":{"id":"3f9a1c"}}`}); err != nil || out != "removed one" || fake.calls[len(fake.calls)-1] != "byid:3f9a1c" {
		t.Fatalf("by id: %q %v %v", out, err, fake.calls)
	}
	if out, err := p.Execute(ctx, []string{`{"cmd":"forget","args":{"match":"tabs"}}`}); err != nil || out != "removed" || fake.calls[len(fake.calls)-1] != "forget:tabs" {
		t.Fatalf("forget: %q %v %v", out, err, fake.calls)
	}
	if _, err := p.Execute(ctx, []string{`{"cmd":"forget","args":{}}`}); err == nil || !strings.Contains(err.Error(), `"id"`) {
		t.Fatalf("forget without match/id must name both: %v", err)
	}

	if !p.IsReadOnly([]string{`{"cmd":"list"}`}) || !p.IsReadOnly([]string{`{"cmd":"forget","args":{"match":"x","dry_run":true}}`}) {
		t.Fatal("list and a dry-run forget are reads")
	}
	if p.IsReadOnly([]string{`{"cmd":"forget","args":{"match":"x"}}`}) || p.IsReadOnly([]string{`{"cmd":"forget","args":{"id":"x"}}`}) {
		t.Fatal("a real forget is gated")
	}
	for _, want := range []string{`"name": "list"`, `"dry_run"`, `"id"`} {
		if !strings.Contains(p.Schema(), want) && !strings.Contains(p.Schema(), strings.ReplaceAll(want, `"name": `, `"name":`)) {
			t.Fatalf("schema missing %s", want)
		}
	}
}

// A plain MemoryAdapter (no FactBrowser) keeps working and says what is missing.
type minimalMemoryAdapter struct{}

func (minimalMemoryAdapter) Remember(string, string) (string, error)         { return "ok", nil }
func (minimalMemoryAdapter) UpdateProfile(map[string]string) (string, error) { return "ok", nil }
func (minimalMemoryAdapter) Forget(string) (string, error)                   { return "removed", nil }
func (minimalMemoryAdapter) Recall(string) (string, error)                   { return "recall", nil }

func TestMemoryListWithoutBrowserIsClear(t *testing.T) {
	SetMemoryAdapter(minimalMemoryAdapter{})
	t.Cleanup(func() { SetMemoryAdapter(nil) })
	p := NewBuiltinMemoryPlugin()
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"list"}`}); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("list without browser: %v", err)
	}
	if out, err := p.Execute(context.Background(), []string{`{"cmd":"forget","args":{"match":"x"}}`}); err != nil || out != "removed" {
		t.Fatalf("plain forget still works: %q %v", out, err)
	}
}
