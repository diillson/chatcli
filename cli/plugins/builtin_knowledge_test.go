/*
 * ChatCLI - @knowledge tool tests.
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

// fakeKnowledgeAdapter records the last call so dispatch can be asserted.
type fakeKnowledgeAdapter struct {
	lastOp     string
	lastQuery  string
	lastSource string
	lastKB     string
	lastTopK   int
	lastOffset int
	lastPrefix string
}

func (f *fakeKnowledgeAdapter) Search(query, kb string, topK int) (string, error) {
	f.lastOp, f.lastQuery, f.lastKB, f.lastTopK = "search", query, kb, topK
	return "search-ok", nil
}
func (f *fakeKnowledgeAdapter) Get(source, kb string, offset int) (string, error) {
	f.lastOp, f.lastSource, f.lastKB, f.lastOffset = "get", source, kb, offset
	return "get-ok", nil
}
func (f *fakeKnowledgeAdapter) TOC(kb, prefix string) (string, error) {
	f.lastOp, f.lastKB, f.lastPrefix = "toc", kb, prefix
	return "toc-ok", nil
}
func (f *fakeKnowledgeAdapter) List() (string, error) {
	f.lastOp = "list"
	return "list-ok", nil
}

func withFakeKnowledgeAdapter(t *testing.T) *fakeKnowledgeAdapter {
	t.Helper()
	fake := &fakeKnowledgeAdapter{}
	SetKnowledgeAdapter(fake)
	t.Cleanup(func() { SetKnowledgeAdapter(nil) })
	return fake
}

func TestKnowledgePlugin_DispatchesEnvelope(t *testing.T) {
	fake := withFakeKnowledgeAdapter(t)
	p := NewBuiltinKnowledgePlugin()

	out, err := p.Execute(context.Background(),
		[]string{`{"cmd":"search","args":{"query":"gateway voice","top_k":12,"kb":"docs"}}`})
	if err != nil || out != "search-ok" {
		t.Fatalf("search: out=%q err=%v", out, err)
	}
	if fake.lastQuery != "gateway voice" || fake.lastTopK != 12 || fake.lastKB != "docs" {
		t.Errorf("search args not forwarded: %+v", fake)
	}

	if _, err := p.Execute(context.Background(),
		[]string{`{"cmd":"get","args":{"source":"docs/a.md","offset":100}}`}); err != nil {
		t.Fatal(err)
	}
	if fake.lastOp != "get" || fake.lastSource != "docs/a.md" || fake.lastOffset != 100 {
		t.Errorf("get args not forwarded: %+v", fake)
	}

	if _, err := p.Execute(context.Background(), []string{`{"cmd":"toc","args":{"prefix":"docs/"}}`}); err != nil {
		t.Fatal(err)
	}
	if fake.lastOp != "toc" || fake.lastPrefix != "docs/" {
		t.Errorf("toc args not forwarded: %+v", fake)
	}

	if out, err := p.Execute(context.Background(), []string{`{"cmd":"list"}`}); err != nil || out != "list-ok" {
		t.Fatalf("list: out=%q err=%v", out, err)
	}
}

func TestKnowledgePlugin_AliasesAndArgvForm(t *testing.T) {
	fake := withFakeKnowledgeAdapter(t)
	p := NewBuiltinKnowledgePlugin()

	// Flat JSON without the args wrapper + cmd alias.
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"find","query":"auth"}`}); err != nil {
		t.Fatal(err)
	}
	if fake.lastOp != "search" || fake.lastQuery != "auth" {
		t.Errorf("alias/flat form failed: %+v", fake)
	}

	// argv form: subcommand + --flags.
	if _, err := p.Execute(context.Background(), []string{"read", "--source", "docs/a.md"}); err != nil {
		t.Fatal(err)
	}
	if fake.lastOp != "get" || fake.lastSource != "docs/a.md" {
		t.Errorf("argv form failed: %+v", fake)
	}
}

func TestKnowledgePlugin_Validation(t *testing.T) {
	withFakeKnowledgeAdapter(t)
	p := NewBuiltinKnowledgePlugin()

	if _, err := p.Execute(context.Background(), []string{`{"cmd":"search","args":{}}`}); err == nil ||
		!strings.Contains(err.Error(), `"query" is required`) {
		t.Errorf("search without query must error, got %v", err)
	}
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"get","args":{}}`}); err == nil ||
		!strings.Contains(err.Error(), `"source" is required`) {
		t.Errorf("get without source must error, got %v", err)
	}
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"bogus"}`}); err == nil ||
		!strings.Contains(err.Error(), "unknown cmd") {
		t.Errorf("unknown cmd must error, got %v", err)
	}
	if _, err := p.Execute(context.Background(), nil); err == nil {
		t.Error("empty args must error with an example")
	}
}

func TestKnowledgePlugin_Metadata(t *testing.T) {
	p := NewBuiltinKnowledgePlugin()
	if p.Name() != "@knowledge" || p.Version() == "" || p.Path() != "" {
		t.Errorf("metadata: name=%q version=%q path=%q", p.Name(), p.Version(), p.Path())
	}
	if d := p.Description(); !strings.Contains(d, "knowledge bases") {
		t.Errorf("Description = %q", d)
	}
	if u := p.Usage(); !strings.Contains(u, `"cmd":"search"`) {
		t.Errorf("Usage must show the envelope example, got %q", u)
	}
	var schema struct {
		Subcommands []struct {
			Name string `json:"name"`
		} `json:"subcommands"`
	}
	if err := json.Unmarshal([]byte(p.Schema()), &schema); err != nil {
		t.Fatalf("Schema is not valid JSON: %v", err)
	}
	got := make([]string, 0, len(schema.Subcommands))
	for _, s := range schema.Subcommands {
		got = append(got, s.Name)
	}
	if strings.Join(got, ",") != "search,get,toc,list" {
		t.Errorf("Schema subcommands = %v", got)
	}
}

func TestKnowledgePlugin_NoAdapter(t *testing.T) {
	SetKnowledgeAdapter(nil)
	t.Cleanup(func() { SetKnowledgeAdapter(nil) })
	p := NewBuiltinKnowledgePlugin()
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"list"}`}); err == nil {
		t.Fatal("missing adapter must error, not panic")
	}
}
