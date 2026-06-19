/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pngMagic is the 8-byte PNG file signature.
var pngMagic = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}

func TestDiagramRenderPNG(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "g.png")
	p := NewBuiltinDiagramPlugin()
	args := []string{`{"cmd":"render","dot":"digraph{rankdir=LR; cli->agent; cli->llm}","output":"` + out + `"}`}

	res, err := p.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("render png: %v", err)
	}
	data, rerr := os.ReadFile(out)
	if rerr != nil {
		t.Fatalf("output not written: %v", rerr)
	}
	if len(data) < 8 || !bytes.Equal(data[:8], pngMagic) {
		t.Fatalf("output is not a valid PNG (first bytes: %x)", data[:min(8, len(data))])
	}
	if !strings.Contains(res, out) {
		t.Errorf("summary should mention output path, got: %q", res)
	}
	if !strings.Contains(res, "x") { // dimensions "WxH"
		t.Errorf("summary should include raster dimensions, got: %q", res)
	}
}

func TestDiagramRenderSVG(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "g.svg")
	p := NewBuiltinDiagramPlugin()
	args := []string{`{"dot":"digraph{a->b->c}","format":"svg","output":"` + out + `"}`}

	if _, err := p.Execute(context.Background(), args); err != nil {
		t.Fatalf("render svg: %v", err)
	}
	data, rerr := os.ReadFile(out)
	if rerr != nil {
		t.Fatalf("output not written: %v", rerr)
	}
	if !bytes.Contains(data, []byte("<svg")) {
		t.Fatalf("output is not SVG: %.80s", data)
	}
}

func TestDiagramRenderFromFile(t *testing.T) {
	dir := t.TempDir()
	dotPath := filepath.Join(dir, "in.dot")
	if err := os.WriteFile(dotPath, []byte("digraph{x->y}"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "fromfile.png")
	p := NewBuiltinDiagramPlugin()
	args := []string{`{"file":"` + dotPath + `","output":"` + out + `"}`}
	if _, err := p.Execute(context.Background(), args); err != nil {
		t.Fatalf("render from file: %v", err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("output not written: %v", err)
	}
}

func TestDiagramDefaultTempOutput(t *testing.T) {
	p := NewBuiltinDiagramPlugin()
	res, err := p.Execute(context.Background(), []string{`{"dot":"digraph{a->b}"}`})
	if err != nil {
		t.Fatalf("render to temp: %v", err)
	}
	// The summary must reference a concrete temp path ending in .png.
	if !strings.Contains(res, ".png") {
		t.Fatalf("expected a .png temp path in summary, got: %q", res)
	}
	// Best-effort: the referenced file should exist. Extract the path token.
	for _, tok := range strings.Fields(res) {
		if strings.HasSuffix(tok, ".png") {
			if _, err := os.Stat(tok); err != nil {
				t.Errorf("temp output %q does not exist: %v", tok, err)
			}
			_ = os.Remove(tok)
		}
	}
}

func TestDiagramParseArgs(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCmd    string
		wantFormat string
		wantEngine string
		wantDOT    string
		wantRoot   string
	}{
		{"flat render", []string{`{"dot":"digraph{a->b}","format":"svg"}`}, "render", "svg", "dot", "digraph{a->b}", ""},
		{"envelope", []string{`{"cmd":"render","args":{"dot":"digraph{a->b}","engine":"neato"}}`}, "render", "png", "neato", "digraph{a->b}", ""},
		{"gomod explicit", []string{`{"cmd":"gomod","root":"./x"}`}, "gomod", "png", "dot", "", "./x"},
		{"gomod implied by root", []string{`{"root":"./y"}`}, "gomod", "png", "dot", "", "./y"},
		{"argv", []string{"render", "--format", "svg", "--dot", "digraph{a->b}"}, "render", "svg", "dot", "digraph{a->b}", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseDiagramArgs(tc.args)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got.Cmd != tc.wantCmd {
				t.Errorf("cmd = %q, want %q", got.Cmd, tc.wantCmd)
			}
			if got.Format != tc.wantFormat {
				t.Errorf("format = %q, want %q", got.Format, tc.wantFormat)
			}
			if got.Engine != tc.wantEngine {
				t.Errorf("engine = %q, want %q", got.Engine, tc.wantEngine)
			}
			if got.DOT != tc.wantDOT {
				t.Errorf("dot = %q, want %q", got.DOT, tc.wantDOT)
			}
			if got.Root != tc.wantRoot {
				t.Errorf("root = %q, want %q", got.Root, tc.wantRoot)
			}
		})
	}
}

func TestDiagramValidation(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"bad format", []string{`{"dot":"digraph{a->b}","format":"gif"}`}},
		{"bad engine", []string{`{"dot":"digraph{a->b}","engine":"nope"}`}},
		{"bad style", []string{`{"cmd":"gomod","root":".","style":"neon"}`}},
		{"render without source", []string{`{"format":"png"}`}},
		{"dot and file together", []string{`{"dot":"digraph{a->b}","file":"x.dot"}`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseDiagramArgs(tc.args); err == nil {
				t.Errorf("expected validation error for %s, got nil", tc.name)
			}
		})
	}
}

func TestDiagramDPIClamped(t *testing.T) {
	hi, err := parseDiagramArgs([]string{`{"dot":"digraph{a->b}","dpi":99999}`})
	if err != nil {
		t.Fatal(err)
	}
	if hi.DPI != diagramMaxDPI {
		t.Errorf("dpi high = %d, want clamp to %d", hi.DPI, diagramMaxDPI)
	}
	lo, err := parseDiagramArgs([]string{`{"dot":"digraph{a->b}","dpi":1}`})
	if err != nil {
		t.Fatal(err)
	}
	if lo.DPI != diagramMinDPI {
		t.Errorf("dpi low = %d, want clamp to %d", lo.DPI, diagramMinDPI)
	}
}

func TestDiagramInvalidDOT(t *testing.T) {
	p := NewBuiltinDiagramPlugin()
	_, err := p.Execute(context.Background(), []string{`{"dot":"this is not valid dot {{{"}`})
	if err == nil {
		t.Fatal("expected error rendering invalid DOT, got nil")
	}
}

// TestDiagramGomod builds a real temp Go module with a -> b and asserts the
// generated import graph contains that edge. Uses dotOnly so it never invokes
// the WASM renderer — it exercises the `go list` + DOT-assembly path.
func TestDiagramGomod(t *testing.T) {
	mod := t.TempDir()
	writeFile(t, filepath.Join(mod, "go.mod"), "module example.com/m\n\ngo 1.21\n")
	writeFile(t, filepath.Join(mod, "b", "b.go"), "package b\n\n// B is here.\nfunc B() {}\n")
	writeFile(t, filepath.Join(mod, "a", "a.go"), "package a\n\nimport \"example.com/m/b\"\n\n// A uses B.\nfunc A() { b.B() }\n")

	p := NewBuiltinDiagramPlugin()
	args := []string{`{"cmd":"gomod","root":"` + mod + `","dotOnly":true}`}
	res, err := p.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("gomod: %v", err)
	}
	if !strings.Contains(res, `"a" -> "b"`) {
		t.Fatalf("expected edge \"a\" -> \"b\" in DOT, got:\n%s", res)
	}
	if !strings.HasPrefix(strings.TrimSpace(res), "digraph imports {") {
		t.Errorf("expected a DOT document, got:\n%s", res)
	}
	// Clusters group by top-level segment; a and b are top-level packages so
	// the graph must still render an edge between them.
	if strings.Count(res, "->") != 1 {
		t.Errorf("expected exactly one edge, got:\n%s", res)
	}
}

// TestDiagramGomodRenders renders the temp module to a real PNG end-to-end.
func TestDiagramGomodRenders(t *testing.T) {
	mod := t.TempDir()
	writeFile(t, filepath.Join(mod, "go.mod"), "module example.com/r\n\ngo 1.21\n")
	writeFile(t, filepath.Join(mod, "core", "core.go"), "package core\nfunc C() {}\n")
	writeFile(t, filepath.Join(mod, "app", "app.go"), "package app\nimport \"example.com/r/core\"\nfunc A(){ core.C() }\n")

	out := filepath.Join(mod, "imports.png")
	p := NewBuiltinDiagramPlugin()
	args := []string{`{"cmd":"gomod","root":"` + mod + `","output":"` + out + `"}`}
	if _, err := p.Execute(context.Background(), args); err != nil {
		t.Fatalf("gomod render: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("output not written: %v", err)
	}
	if len(data) < 8 || !bytes.Equal(data[:8], pngMagic) {
		t.Fatalf("gomod output is not a valid PNG")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
