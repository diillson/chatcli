/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocatePatch_ExactAmbiguousAndAll(t *testing.T) {
	content := "a\nfoo()\nb\nfoo()\nc\n"
	if _, err := locatePatch(content, "foo()", "f.go", false); err == nil || !strings.Contains(err.Error(), "occurs 2 times") || !strings.Contains(err.Error(), "lines 2, 4") || !strings.Contains(err.Error(), "--all") {
		t.Fatalf("ambiguous search must be refused with its lines: %v", err)
	}
	m, err := locatePatch(content, "foo()", "f.go", true)
	if err != nil || len(m) != 2 || m[0].line != 2 || m[1].line != 4 {
		t.Fatalf("--all must return every occurrence: %+v %v", m, err)
	}
	if got := applyPatches(content, "foo()", "bar()", m); got != "a\nbar()\nb\nbar()\nc\n" {
		t.Fatalf("apply all = %q", got)
	}
	m, err = locatePatch(content, "b\nfoo()", "f.go", false)
	if err != nil || len(m) != 1 || m[0].line != 3 || m[0].tolerant {
		t.Fatalf("context makes it unique: %+v %v", m, err)
	}
}

func TestLocatePatch_TolerantIndentationReindents(t *testing.T) {
	content := "func x() {\n\tif ok {\n\t\treturn 1\n\t}\n}\n"
	// The model copied the block without the tabs.
	m, err := locatePatch(content, "if ok {\n\treturn 1\n}", "f.go", false)
	if err != nil || len(m) != 1 || !m[0].tolerant || m[0].line != 2 {
		t.Fatalf("tolerant match: %+v %v", m, err)
	}
	got := applyPatches(content, "if ok {\n\treturn 1\n}", "if ok {\n\treturn 2\n}", m)
	if got != "func x() {\n\tif ok {\n\t\treturn 2\n\t}\n}\n" {
		t.Fatalf("re-indented replacement = %q", got)
	}
	// Two indentation-only candidates are ambiguous too.
	twice := "\tfoo()\n\t\tfoo()\n"
	if _, err := locatePatch(twice, "foo()\n", "f.go", false); err == nil || !strings.Contains(err.Error(), "occurs 2 times") {
		t.Fatalf("exact matches win and are ambiguous: %v", err)
	}
}

func TestLocatePatch_NotFoundNamesClosestLine(t *testing.T) {
	content := "package x\n\nfunc Login(user string) error {\n\treturn nil\n}\n"
	_, err := locatePatch(content, "func Login(user, pass string) error {", "auth.go", false)
	if err == nil {
		t.Fatal("expected not found")
	}
	for _, want := range []string{"not found in auth.go", "closest line is 3", "func Login(user string) error {", "copy the text exactly"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("hint missing %q: %v", want, err)
		}
	}
	if _, err := locatePatch(content, "zzz qqq", "auth.go", false); err == nil || strings.Contains(err.Error(), "closest") {
		t.Fatalf("no shared word: no closest-line hint, got %v", err)
	}
	if _, err := locatePatch(content, "", "auth.go", false); err == nil {
		t.Fatal("empty search must error")
	}
}

func TestHandlePatch_AmbiguousRefusedAndAllApplies(t *testing.T) {
	e, out, root := newWsEngine(t)
	target := filepath.Join(root, "f.txt")
	if err := os.WriteFile(target, []byte("x = 1\ny = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := e.Execute(context.Background(), "patch", []string{"--file", target, "--search", "= 1", "--replace", "= 2"})
	if err == nil || !strings.Contains(err.Error(), "occurs 2 times") {
		t.Fatalf("ambiguous patch must be refused: %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "x = 1\ny = 1\n" {
		t.Fatalf("file must be untouched after a refusal: %q", got)
	}
	if err := e.Execute(context.Background(), "patch", []string{"--file", target, "--search", "= 1", "--replace", "= 2", "--all"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(target); string(got) != "x = 2\ny = 2\n" {
		t.Fatalf("--all = %q", got)
	}
	if !strings.Contains(out.String(), "2 occurrences at lines 1, 2") {
		t.Fatalf("result must say where it landed: %q", out.String())
	}
	err = e.Execute(context.Background(), "patch", []string{"--file", target, "--search", "x = 9", "--replace", "x = 3"})
	if err == nil || !strings.Contains(err.Error(), "closest line is 1") {
		t.Fatalf("not found must name the closest line: %v", err)
	}
	if err := e.Execute(context.Background(), "patch", []string{"--file", target}); err == nil || !strings.Contains(err.Error(), "--search") {
		t.Fatalf("missing flags must be named: %v", err)
	}
	// An empty --replace deletes the text: a legitimate edit, not an error.
	if err := e.Execute(context.Background(), "patch", []string{"--file", target, "--search", "x = 2\n", "--replace", ""}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(target); string(got) != "y = 2\n" {
		t.Fatalf("deletion = %q", got)
	}
}

func TestMultipatch_AmbiguousEditRefusedAtomically(t *testing.T) {
	e, _, root := newWsEngine(t)
	a := filepath.Join(root, "a.txt")
	if err := os.WriteFile(a, []byte("k=1\nk=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := e.Execute(context.Background(), "multipatch", []string{"--edits", `[{"file":"` + a + `","search":"k=1","replace":"k=2"}]`})
	if err == nil || !strings.Contains(err.Error(), "occurs 2 times") {
		t.Fatalf("multipatch must refuse an ambiguous edit: %v", err)
	}
	if got, _ := os.ReadFile(a); string(got) != "k=1\nk=1\n" {
		t.Fatalf("file must be untouched: %q", got)
	}
}

func TestCapSearchOutputAndCommands(t *testing.T) {
	out := "a.go:1:1:foo\na.go:2:1:foo\nb.go:3:1:foo\n"
	if got := capSearchOutput(out, 5); got != out {
		t.Fatalf("under the cap must pass through: %q", got)
	}
	got := capSearchOutput(out, 2)
	if strings.Contains(got, "b.go:3") || !strings.Contains(got, "1 more match(es) not shown (--max-results 2)") {
		t.Fatalf("cap = %q", got)
	}
	withCtx := "a.go:1:1:foo\na.go-2-ctx\nb.go:3:1:foo\n"
	if got := capSearchOutput(withCtx, 1); strings.Contains(got, "b.go") || !strings.Contains(got, "a.go-2-ctx") {
		t.Fatalf("context lines stay with their match: %q", got)
	}
	for _, c := range Commands {
		if !strings.Contains(GetMetadata().Usage, c) {
			t.Fatalf("usage omits %s", c)
		}
	}
	if !strings.Contains(GetSchema(), "--all") {
		t.Fatal("schema must document patch --all")
	}
}
