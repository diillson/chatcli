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

// fakeLSPRefactorAdapter adds the optional navigator + renamer capabilities
// on top of the base fake and records what the plugin dispatched.
type fakeLSPRefactorAdapter struct {
	fakeLSPAdapter
	query   string
	newName string
	dryRun  bool
}

func (f *fakeLSPRefactorAdapter) Implementation(file string, line, column int) (string, error) {
	f.lastCall, f.file, f.line, f.column = "implementation", file, line, column
	return "impls", nil
}
func (f *fakeLSPRefactorAdapter) WorkspaceSymbol(file, query string, limit int) (string, error) {
	f.lastCall, f.file, f.query, f.limit = "workspace_symbol", file, query, limit
	return "wsyms", nil
}
func (f *fakeLSPRefactorAdapter) Rename(file string, line, column int, newName string, dryRun bool) (string, error) {
	f.lastCall, f.file, f.line, f.column, f.newName, f.dryRun = "rename", file, line, column, newName, dryRun
	if dryRun {
		return "preview", nil
	}
	return "applied", nil
}

func withFakeLSPRefactor(t *testing.T) *fakeLSPRefactorAdapter {
	t.Helper()
	fake := &fakeLSPRefactorAdapter{}
	SetLSPAdapter(fake)
	t.Cleanup(func() { SetLSPAdapter(nil) })
	return fake
}

func TestLSPRefactorVerbsDispatch(t *testing.T) {
	fake := withFakeLSPRefactor(t)
	p := NewBuiltinLSPPlugin()
	cases := []struct {
		name    string
		args    []string
		call    string
		want    string
		dryRun  bool
		newName string
		query   string
		limit   int
	}{
		{"implementation envelope", []string{`{"cmd":"implementation","args":{"file":"a.go","line":"7","column":3}}`}, "implementation", "impls", false, "", "", 0},
		{"impl alias argv", []string{"impl", "--file", "a.go", "--line", "7", "--column", "3"}, "implementation", "impls", false, "", "", 0},
		{"workspace_symbol envelope", []string{`{"cmd":"workspace_symbol","args":{"query":"Chat","file":"cli/cli.go","limit":5}}`}, "workspace_symbol", "wsyms", false, "", "Chat", 5},
		{"workspace_symbol positional", []string{"workspace_symbol", "Chat", "CLI"}, "workspace_symbol", "wsyms", false, "", "Chat CLI", 0},
		{"workspace_symbol flags", []string{"search", "--query", "Chat"}, "workspace_symbol", "wsyms", false, "", "Chat", 0},
		{"rename defaults to dry run", []string{`{"cmd":"rename","args":{"file":"a.go","line":7,"column":3,"new_name":"newer"}}`}, "rename", "preview", true, "newer", "", 0},
		{"rename explicit false", []string{`{"cmd":"rename","args":{"file":"a.go","line":7,"column":3,"new_name":"newer","dry_run":false}}`}, "rename", "applied", false, "newer", "", 0},
		{"rename stringified false", []string{`{"cmd":"rename","args":{"file":"a.go","line":7,"column":3,"newName":"newer","dry_run":"false"}}`}, "rename", "applied", false, "newer", "", 0},
		{"rename argv bare dry_run", []string{"rename", "--file", "a.go", "--line", "7", "--column", "3", "--new_name", "newer", "--dry_run"}, "rename", "preview", true, "newer", "", 0},
		{"rename argv dry_run false", []string{"rename", "--file", "a.go", "--line", "7", "--column", "3", "--new_name", "newer", "--dry_run", "false"}, "rename", "applied", false, "newer", "", 0},
		{"rename positional", []string{"rename", "a.go", "7", "3", "newer"}, "rename", "preview", true, "newer", "", 0},
		{"rename apply alias", []string{`{"cmd":"rename","args":{"file":"a.go","line":7,"column":3,"new_name":"newer","apply":true}}`}, "rename", "applied", false, "newer", "", 0},
	}
	for _, c := range cases {
		*fake = fakeLSPRefactorAdapter{}
		out, err := p.Execute(context.Background(), c.args)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if out != c.want || fake.lastCall != c.call {
			t.Fatalf("%s: out=%q call=%q", c.name, out, fake.lastCall)
		}
		if c.call == "rename" && (fake.dryRun != c.dryRun || fake.newName != c.newName || fake.line != 7 || fake.column != 3) {
			t.Fatalf("%s: dryRun=%v newName=%q line=%d column=%d", c.name, fake.dryRun, fake.newName, fake.line, fake.column)
		}
		if c.call == "workspace_symbol" && (fake.query != c.query || fake.limit != c.limit) {
			t.Fatalf("%s: query=%q limit=%d", c.name, fake.query, fake.limit)
		}
	}
}

func TestLSPRefactorVerbsValidate(t *testing.T) {
	withFakeLSPRefactor(t)
	p := NewBuiltinLSPPlugin()
	cases := []struct {
		args []string
		want string
	}{
		{[]string{`{"cmd":"rename","args":{"file":"a.go","line":7,"column":3}}`}, `"new_name" is required`},
		{[]string{`{"cmd":"rename","args":{"file":"a.go","new_name":"x"}}`}, `"line" and "column"`},
		{[]string{`{"cmd":"workspace_symbol","args":{"file":"a.go"}}`}, `"query" is required`},
		{[]string{`{"cmd":"implementation","args":{"line":1,"column":1}}`}, `"file" is required`},
	}
	for _, c := range cases {
		if _, err := p.Execute(context.Background(), c.args); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: err=%v want %q", c.args, err, c.want)
		}
	}
}

func TestLSPRefactorVerbsNeedTheOptionalCapability(t *testing.T) {
	withFakeLSP(t) // base adapter only
	p := NewBuiltinLSPPlugin()
	for _, args := range [][]string{
		{`{"cmd":"implementation","args":{"file":"a.go","line":1,"column":1}}`},
		{`{"cmd":"workspace_symbol","args":{"query":"x"}}`},
		{`{"cmd":"rename","args":{"file":"a.go","line":1,"column":1,"new_name":"y"}}`},
	} {
		if _, err := p.Execute(context.Background(), args); err == nil || !strings.Contains(err.Error(), "not available") {
			t.Errorf("%v: expected a capability error, got %v", args, err)
		}
	}
	if !strings.Contains(p.Usage(), "workspace_symbol") || !strings.Contains(p.Schema(), `"rename"`) || !strings.Contains(p.Schema(), "dry_run") {
		t.Fatal("Usage/Schema must document the new verbs")
	}
}

func TestLSPCapsRenameApplyIsMutating(t *testing.T) {
	checkCaps(t, "@lsp", NewBuiltinLSPPlugin(), []capsCase{
		{[]string{`{"cmd":"references","args":{"file":"a.go","line":1,"column":1}}`}, true, true},
		{[]string{`{"cmd":"implementation","args":{"file":"a.go","line":1,"column":1}}`}, true, true},
		{[]string{`{"cmd":"workspace_symbol","args":{"query":"x"}}`}, true, true},
		{[]string{`{"cmd":"rename","args":{"file":"a.go","line":1,"column":1,"new_name":"y"}}`}, true, true},
		{[]string{`{"cmd":"rename","args":{"file":"a.go","line":1,"column":1,"new_name":"y","dry_run":false}}`}, false, false},
		{[]string{"rename", "--file", "a.go", "--line", "1", "--column", "1", "--new_name", "y", "--dry_run", "false"}, false, false},
		{[]string{`{not json`}, false, false},
	})
	p := NewBuiltinLSPPlugin()
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{`{"cmd":"implementation","args":{"file":"a.go","line":4,"column":1}}`}, "describe_implementation"},
		{[]string{`{"cmd":"workspace_symbol","args":{"query":"Chat"}}`}, "Chat"},
		{[]string{`{"cmd":"rename","args":{"file":"a.go","line":4,"column":1,"new_name":"y"}}`}, "describe_rename_preview"},
		{[]string{`{"cmd":"rename","args":{"file":"a.go","line":4,"column":1,"new_name":"y","dry_run":false}}`}, "describe_rename"},
	} {
		if got := p.DescribeCall(c.args); !strings.Contains(got, c.want) {
			t.Errorf("DescribeCall(%v) = %q, want %q", c.args, got, c.want)
		}
	}
	if got := p.DescribeCall([]string{`{"cmd":"rename","args":{"file":"a.go","line":4,"column":1,"new_name":"y","dry_run":false}}`}); strings.Contains(got, "preview") {
		t.Fatalf("apply must not describe as preview: %q", got)
	}
}
