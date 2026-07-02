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

// fakeLSPAdapter records the dispatched call so tests can assert the plugin's
// parsing/dispatch layer without any real language server.
type fakeLSPAdapter struct {
	lastCall string
	file     string
	line     int
	column   int
	incl     bool
	limit    int
}

func (f *fakeLSPAdapter) Diagnostics(file string) (string, error) {
	f.lastCall, f.file = "diagnostics", file
	return "diags", nil
}
func (f *fakeLSPAdapter) Definition(file string, line, column int) (string, error) {
	f.lastCall, f.file, f.line, f.column = "definition", file, line, column
	return "def", nil
}
func (f *fakeLSPAdapter) References(file string, line, column int, incl bool, limit int) (string, error) {
	f.lastCall, f.file, f.line, f.column, f.incl, f.limit = "references", file, line, column, incl, limit
	return "refs", nil
}
func (f *fakeLSPAdapter) Symbols(file string) (string, error) {
	f.lastCall, f.file = "symbols", file
	return "syms", nil
}
func (f *fakeLSPAdapter) Hover(file string, line, column int) (string, error) {
	f.lastCall, f.file, f.line, f.column = "hover", file, line, column
	return "hover", nil
}

func withFakeLSP(t *testing.T) *fakeLSPAdapter {
	t.Helper()
	fake := &fakeLSPAdapter{}
	SetLSPAdapter(fake)
	t.Cleanup(func() { SetLSPAdapter(nil) })
	return fake
}

func TestLSPEnvelopeDispatch(t *testing.T) {
	fake := withFakeLSP(t)
	p := NewBuiltinLSPPlugin()

	out, err := p.Execute(context.Background(),
		[]string{`{"cmd":"references","args":{"file":"cli/cli.go","line":128,"column":14,"include_declaration":false,"limit":10}}`})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out != "refs" || fake.lastCall != "references" {
		t.Fatalf("dispatched %q/%q, want references", out, fake.lastCall)
	}
	if fake.file != "cli/cli.go" || fake.line != 128 || fake.column != 14 || fake.incl || fake.limit != 10 {
		t.Fatalf("args not threaded: %+v", fake)
	}
}

func TestLSPAliasesFold(t *testing.T) {
	fake := withFakeLSP(t)
	p := NewBuiltinLSPPlugin()

	for alias, want := range map[string]string{
		"diag": "diagnostics", "check": "diagnostics",
		"def": "definition", "goto": "definition",
		"refs": "references", "usages": "references",
		"outline": "symbols",
		"type":    "hover", "signature": "hover",
	} {
		args := `{"cmd":"` + alias + `","args":{"file":"a.go","line":1,"column":1}}`
		if _, err := p.Execute(context.Background(), []string{args}); err != nil {
			t.Fatalf("alias %q: %v", alias, err)
		}
		if fake.lastCall != want {
			t.Fatalf("alias %q dispatched %q, want %q", alias, fake.lastCall, want)
		}
	}
}

func TestLSPArgvForm(t *testing.T) {
	fake := withFakeLSP(t)
	p := NewBuiltinLSPPlugin()

	if _, err := p.Execute(context.Background(), []string{"definition", "pkg/x.go", "42", "7"}); err != nil {
		t.Fatalf("argv form: %v", err)
	}
	if fake.lastCall != "definition" || fake.file != "pkg/x.go" || fake.line != 42 || fake.column != 7 {
		t.Fatalf("argv args not threaded: %+v", fake)
	}
}

func TestLSPValidationErrors(t *testing.T) {
	withFakeLSP(t)
	p := NewBuiltinLSPPlugin()

	cases := []struct {
		args []string
		want string
	}{
		{[]string{`{"cmd":"diagnostics","args":{}}`}, `"file" is required`},
		{[]string{`{"cmd":"definition","args":{"file":"a.go"}}`}, "1-based"},
		{[]string{`{"cmd":"definition","args":{"file":"a.go","line":0,"column":3}}`}, "1-based"},
		{[]string{`{"cmd":"rename","args":{"file":"a.go"}}`}, "unknown cmd"},
	}
	for _, c := range cases {
		if _, err := p.Execute(context.Background(), c.args); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("args %v: err = %v, want contains %q", c.args, err, c.want)
		}
	}
}

func TestLSPNoAdapterWired(t *testing.T) {
	SetLSPAdapter(nil)
	p := NewBuiltinLSPPlugin()
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"symbols","args":{"file":"a.go"}}`}); err == nil {
		t.Fatal("missing adapter must error, not panic")
	}
}

func TestLSPCapsReadOnlyAndConcurrent(t *testing.T) {
	p := NewBuiltinLSPPlugin()
	if !p.IsReadOnly(nil) || !p.IsConcurrencySafe(nil) {
		t.Fatal("@lsp must advertise read-only + concurrency-safe")
	}
	if p.Schema() == "" || !strings.Contains(p.Schema(), "diagnostics") {
		t.Fatal("schema must describe subcommands")
	}
}
