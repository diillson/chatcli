/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * commands_outline_test.go
 *
 * outline: exact go/ast skeletons for Go, pattern skeletons for other
 * languages. map: budgeted, deterministically ranked repo structure.
 */
package engine

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const outlineGoFixture = `package sample

import "fmt"

const answer = 42

var Verbose bool

type Greeter struct{ Name string }

type Speaker interface{ Speak() string }

func Hello(name string, times int) (string, error) {
	return fmt.Sprintf("hi %s", name), nil
}

func (g *Greeter) Speak() string { return g.Name }
`

func newOutlineEngine(t *testing.T) (*Engine, *bytes.Buffer, string) {
	t.Helper()
	dir := t.TempDir()
	var out bytes.Buffer
	e := NewEngine(&out, &out, dir)
	return e, &out, dir
}

func TestOutlineGoFile(t *testing.T) {
	e, out, dir := newOutlineEngine(t)
	path := filepath.Join(dir, "sample.go")
	if err := os.WriteFile(path, []byte(outlineGoFixture), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := e.Execute(context.Background(), "outline", []string{"--file", path}); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"go/ast", "const answer", "var Verbose", "struct Greeter",
		"interface Speaker", "func Hello(string, int) (string, error)",
		"func (*Greeter) Speak() string",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("outline missing %q:\n%s", want, text)
		}
	}
}

func TestOutlineGenericFile(t *testing.T) {
	e, out, dir := newOutlineEngine(t)
	path := filepath.Join(dir, "app.py")
	py := "import os\n\nclass Server:\n    def start(self):\n        pass\n\ndef main():\n    pass\n"
	if err := os.WriteFile(path, []byte(py), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := e.Execute(context.Background(), "outline", []string{"--file", path}); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"pattern", "class Server", "def main"} {
		if !strings.Contains(text, want) {
			t.Fatalf("generic outline missing %q:\n%s", want, text)
		}
	}
}

func TestOutlineRequiresFile(t *testing.T) {
	e, _, _ := newOutlineEngine(t)
	if err := e.Execute(context.Background(), "outline", nil); err == nil {
		t.Fatal("outline without --file must error")
	}
}

func TestMapRanksAndBudgets(t *testing.T) {
	e, out, dir := newOutlineEngine(t)
	// big.go carries more declarations than small.go and must render first.
	big := "package a\n"
	for i := 0; i < 12; i++ {
		big += "func F" + string(rune('A'+i)) + "() {}\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "big.go"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "small.go"), []byte("package a\nfunc One() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Ignored dirs and tests must not appear.
	if err := os.MkdirAll(filepath.Join(dir, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "node_modules", "x.js"), []byte("function hidden() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "big_test.go"), []byte("package a\nfunc TestX(t *T) {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := e.Execute(context.Background(), "map", []string{"--dir", dir}); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "big.go") || !strings.Contains(text, "small.go") {
		t.Fatalf("map missing files:\n%s", text)
	}
	if strings.Index(text, "big.go") > strings.Index(text, "small.go") {
		t.Fatalf("ranking broken — most structure must come first:\n%s", text)
	}
	if strings.Contains(text, "hidden") || strings.Contains(text, "TestX") {
		t.Fatalf("ignored/test files leaked into the map:\n%s", text)
	}

	// Tight budget: elision must be explicit.
	out.Reset()
	if err := e.Execute(context.Background(), "map", []string{"--dir", dir, "--budget", "120"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "beyond the budget") {
		t.Fatalf("budget elision must be explicit:\n%s", out.String())
	}
}

func TestMapEmptyDir(t *testing.T) {
	e, out, dir := newOutlineEngine(t)
	if err := e.Execute(context.Background(), "map", []string{"--dir", dir}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "No source files") {
		t.Fatalf("empty dir message missing:\n%s", out.String())
	}
}
