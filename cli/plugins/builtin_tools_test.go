/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"context"
	"strings"
	"testing"
)

type fakeToolCatalog struct{ described string }

func (f *fakeToolCatalog) Describe(name string) (string, error) {
	f.described = name
	return "FULL DEF OF " + name, nil
}
func (f *fakeToolCatalog) List() (string, error) { return "INDEX", nil }

func withFakeCatalog(t *testing.T) *fakeToolCatalog {
	t.Helper()
	fake := &fakeToolCatalog{}
	SetToolCatalogAdapter(fake)
	t.Cleanup(func() { SetToolCatalogAdapter(nil) })
	return fake
}

func TestToolsDescribeDispatch(t *testing.T) {
	fake := withFakeCatalog(t)
	p := NewBuiltinToolsPlugin()

	out, err := p.Execute(context.Background(), []string{`{"cmd":"describe","args":{"name":"@diagram"}}`})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out != "FULL DEF OF @diagram" || fake.described != "@diagram" {
		t.Fatalf("describe not dispatched: %q / %q", out, fake.described)
	}
}

func TestToolsListAndAliases(t *testing.T) {
	withFakeCatalog(t)
	p := NewBuiltinToolsPlugin()

	for _, alias := range []string{"list", "ls", "index", "catalog"} {
		out, err := p.Execute(context.Background(), []string{`{"cmd":"` + alias + `"}`})
		if err != nil || out != "INDEX" {
			t.Fatalf("alias %q: out=%q err=%v", alias, out, err)
		}
	}
	// describe aliases + argv form
	fake := withFakeCatalog(t)
	if _, err := p.Execute(context.Background(), []string{"help", "@lsp"}); err != nil {
		t.Fatalf("argv describe: %v", err)
	}
	if fake.described != "@lsp" {
		t.Fatalf("argv name not threaded: %q", fake.described)
	}
}

// The agent-mode flattener converts {"cmd":"describe","args":{"name":"X"}}
// into argv ["describe","--name","X"]; the argv path must parse flags instead
// of taking "--name" as the tool name.
func TestToolsDescribeArgvFlagForms(t *testing.T) {
	p := NewBuiltinToolsPlugin()
	cases := []struct {
		argv []string
		want string
	}{
		{[]string{"describe", "--name", "@graphview"}, "@graphview"},
		{[]string{"describe", "--name=@graphview"}, "@graphview"},
		{[]string{"describe", "--name", "graphview"}, "graphview"},
		{[]string{"describe", "@graphview"}, "@graphview"},
	}
	for _, tc := range cases {
		fake := withFakeCatalog(t)
		if _, err := p.Execute(context.Background(), tc.argv); err != nil {
			t.Fatalf("%v: %v", tc.argv, err)
		}
		if fake.described != tc.want {
			t.Errorf("%v: described %q, want %q", tc.argv, fake.described, tc.want)
		}
	}
	// Flag with no value must still fail with the actionable "name" error,
	// not leak "--name" to the catalog as a tool name.
	withFakeCatalog(t)
	if _, err := p.Execute(context.Background(), []string{"describe", "--name"}); err == nil || !strings.Contains(err.Error(), `"name" is required`) {
		t.Fatalf("dangling flag must error on missing name, got %v", err)
	}
}

func TestToolsValidation(t *testing.T) {
	withFakeCatalog(t)
	p := NewBuiltinToolsPlugin()

	if _, err := p.Execute(context.Background(), []string{`{"cmd":"describe","args":{}}`}); err == nil || !strings.Contains(err.Error(), `"name" is required`) {
		t.Fatalf("missing name must error, got %v", err)
	}
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"install"}`}); err == nil || !strings.Contains(err.Error(), "unknown cmd") {
		t.Fatalf("unknown cmd must error, got %v", err)
	}
	SetToolCatalogAdapter(nil)
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"list"}`}); err == nil {
		t.Fatal("missing adapter must error, not panic")
	}
}

func TestToolsCaps(t *testing.T) {
	p := NewBuiltinToolsPlugin()
	if !p.IsReadOnly(nil) || !p.IsConcurrencySafe(nil) {
		t.Fatal("@tools must advertise read-only + concurrency-safe")
	}
}
