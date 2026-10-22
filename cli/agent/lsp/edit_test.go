/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package lsp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImplementationAndWorkspaceSymbolsDecode(t *testing.T) {
	c := newNavClient(t, false)
	locs, err := c.Implementation("file:///src/a.go", Position{Line: 3, Character: 1})
	if err != nil || len(locs) != 1 || locs[0].URI != "file:///src/impl.go" || locs[0].Range.Start.Line != 42 {
		t.Fatalf("implementation: locs=%+v err=%v", locs, err)
	}
	syms, err := c.WorkspaceSymbols("Manager")
	if err != nil || len(syms) != 2 {
		t.Fatalf("workspace symbols: %+v err=%v", syms, err)
	}
	if syms[0].URI != "file:///src/a.go" || syms[0].Line != 10 || syms[0].KindLabel() != "struct" || syms[0].Container != "ctxmgr" {
		t.Fatalf("first symbol wrong: %+v", syms[0])
	}
	if syms[1].URI != "file:///src/opt.go" || syms[1].Line != 0 || syms[1].KindLabel() != "function" {
		t.Fatalf("uri-only location must still decode: %+v", syms[1])
	}
}

func TestRenameDecodesBothWorkspaceEditShapes(t *testing.T) {
	c := newNavClient(t, false)
	edit, err := c.Rename("file:///src/a.go", Position{Line: 3, Character: 1}, "Renamed")
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if len(edit.Changes) != 2 || len(edit.Changes["file:///src/a.go"]) != 1 || len(edit.Changes["file:///src/b.go"]) != 1 {
		t.Fatalf("expected edits for a.go and b.go, got %+v", edit.Changes)
	}
	if e := edit.Changes["file:///src/a.go"][0]; e.NewText != "Renamed" || e.Range.Start.Line != 3 || e.Range.End.Character != 4 {
		t.Fatalf("documentChanges edit wrong: %+v", e)
	}
}

func TestDecodeWorkspaceEditReportsResourceOps(t *testing.T) {
	raw := json.RawMessage(`{"documentChanges":[{"kind":"rename","oldUri":"file:///a","newUri":"file:///b"},{"kind":"delete","uri":"file:///c"}]}`)
	edit := decodeWorkspaceEdit(raw)
	if len(edit.Changes) != 0 || len(edit.Unsupported) != 2 || !strings.HasPrefix(edit.Unsupported[1], "delete") {
		t.Fatalf("resource ops must be reported, got %+v", edit)
	}
	if e := decodeWorkspaceEdit(json.RawMessage("null")); len(e.Changes) != 0 {
		t.Fatalf("null must decode to an empty edit")
	}
}

func TestApplyTextEditsUTF16AndOrdering(t *testing.T) {
	// "é" is one UTF-16 unit but two bytes; "😀" is two units, four bytes.
	content := "héllo 😀 old\nold two\r\nend"
	edits := []TextEdit{
		{Range: Range{Start: Position{Line: 1, Character: 0}, End: Position{Line: 1, Character: 3}}, NewText: "new"},
		{Range: Range{Start: Position{Line: 0, Character: 9}, End: Position{Line: 0, Character: 12}}, NewText: "new"},
	}
	got, err := ApplyTextEdits(content, edits)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if want := "héllo 😀 new\nnew two\r\nend"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	cases := []struct {
		name  string
		edits []TextEdit
	}{
		{"overlap", []TextEdit{
			{Range: Range{Start: Position{0, 0}, End: Position{0, 5}}, NewText: "a"},
			{Range: Range{Start: Position{0, 3}, End: Position{0, 7}}, NewText: "b"},
		}},
		{"line past eof", []TextEdit{{Range: Range{Start: Position{9, 0}, End: Position{9, 1}}}}},
		{"column past eol", []TextEdit{{Range: Range{Start: Position{2, 0}, End: Position{2, 40}}}}},
		{"end before start", []TextEdit{{Range: Range{Start: Position{0, 5}, End: Position{0, 1}}}}},
	}
	for _, c := range cases {
		if _, err := ApplyTextEdits(content, c.edits); err == nil {
			t.Errorf("%s: expected an error", c.name)
		}
	}
}

func TestApplyWorkspaceEditWritesInsideRootOnly(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "a.go")
	sub := filepath.Join(root, "pkg")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatal(err)
	}
	b := filepath.Join(sub, "b.go")
	for _, f := range []string{a, b} {
		if err := os.WriteFile(f, []byte("func old() {}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rng := Range{Start: Position{0, 5}, End: Position{0, 8}}
	edit := WorkspaceEdit{Changes: map[string][]TextEdit{
		"file://" + filepath.ToSlash(a): {{Range: rng, NewText: "renamed"}},
		"file://" + filepath.ToSlash(b): {{Range: rng, NewText: "renamed"}},
	}}
	touched, err := ApplyWorkspaceEdit(root, edit)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(touched) != 2 || touched[0] != "a.go" || touched[1] != filepath.Join("pkg", "b.go") {
		t.Fatalf("touched = %v", touched)
	}
	for _, f := range []string{a, b} {
		data, _ := os.ReadFile(f)
		if string(data) != "func renamed() {}\n" {
			t.Fatalf("%s not rewritten: %q", f, data)
		}
	}

	// Outside the root: refused before anything is written.
	outside := filepath.Join(t.TempDir(), "x.go")
	if err := os.WriteFile(outside, []byte("func old() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a, []byte("func old() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bad := WorkspaceEdit{Changes: map[string][]TextEdit{
		"file://" + filepath.ToSlash(a):       {{Range: rng, NewText: "renamed"}},
		"file://" + filepath.ToSlash(outside): {{Range: rng, NewText: "renamed"}},
	}}
	if _, err := ApplyWorkspaceEdit(root, bad); err == nil || !strings.Contains(err.Error(), "outside the workspace") {
		t.Fatalf("expected boundary refusal, got %v", err)
	}
	if data, _ := os.ReadFile(a); string(data) != "func old() {}\n" {
		t.Fatalf("nothing may be written when one target is refused: %q", data)
	}

	// Resource operations refuse the apply.
	if _, err := ApplyWorkspaceEdit(root, WorkspaceEdit{Unsupported: []string{"delete file:///x"}}); err == nil {
		t.Fatal("unsupported ops must refuse")
	}

	// A range that no longer fits (file changed since analysis) refuses.
	if err := os.WriteFile(a, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyWorkspaceEdit(root, WorkspaceEdit{Changes: map[string][]TextEdit{
		"file://" + filepath.ToSlash(a): {{Range: rng, NewText: "renamed"}},
	}}); err == nil || !strings.Contains(err.Error(), "change") {
		t.Fatalf("expected a changed-file refusal, got %v", err)
	}
}

func TestApplyWorkspaceEditKeepsFileMode(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "run.sh")
	if err := os.WriteFile(a, []byte("old\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	rng := Range{Start: Position{0, 0}, End: Position{0, 3}}
	edit := WorkspaceEdit{Changes: map[string][]TextEdit{"file://" + filepath.ToSlash(a): {{Range: rng, NewText: "new"}}}}
	if _, err := ApplyWorkspaceEdit(root, edit); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(a)
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("mode not preserved: %v", info.Mode())
	}
	if data, _ := os.ReadFile(a); string(data) != "new\n" {
		t.Fatalf("got %q", data)
	}
}

func TestURIToPath(t *testing.T) {
	if got := URIToPath("file:///tmp/a%20b/c.go"); got != filepath.FromSlash("/tmp/a b/c.go") {
		t.Fatalf("got %q", got)
	}
	if got := URIToPath("/plain/path.go"); got != "/plain/path.go" {
		t.Fatalf("got %q", got)
	}
}

func TestSpliceAtRuneBoundaries(t *testing.T) {
	out, err := spliceAtRuneBoundaries("héllo", 1, 3, "e")
	if err != nil || out != "hello" {
		t.Fatalf("splice = %q %v", out, err)
	}
	if _, err := spliceAtRuneBoundaries("héllo", 2, 3, "x"); err == nil {
		t.Fatal("an offset inside a multi-byte character must be refused")
	}
	if _, err := spliceAtRuneBoundaries("abc", 2, 9, "x"); err == nil {
		t.Fatal("an offset past the end must be refused")
	}
	if out, err := spliceAtRuneBoundaries("abc", 3, 3, "d"); err != nil || out != "abcd" {
		t.Fatalf("append at the end = %q %v", out, err)
	}
}
