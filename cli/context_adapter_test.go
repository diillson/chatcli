/*
 * ChatCLI - @context adapter tests.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diillson/chatcli/llm/embedding"
	"go.uber.org/zap"
)

// newContextTestCLI builds a minimal ChatCLI whose context manager lives under
// a throwaway HOME, plus a docs-flatten JSONL corpus on disk to build from.
func newContextTestCLI(t *testing.T) (*ChatCLI, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())

	handler, err := NewContextHandler(zap.NewNop())
	if err != nil {
		t.Fatalf("NewContextHandler: %v", err)
	}
	handler.GetManager().AttachEmbeddingProvider(embedding.NewNull())

	corpus := filepath.Join(t.TempDir(), "docs.jsonl")
	lines := strings.Join([]string{
		`{"id":"guide/install.md#0001","source":"guide/install.md","content":"# Install\nHow to install the CLI."}`,
		`{"id":"api/auth.md#0001","source":"api/auth.md","content":"# Auth\noauth login bearer token flows"}`,
	}, "\n")
	if err := os.WriteFile(corpus, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	return &ChatCLI{contextHandler: handler, logger: zap.NewNop()}, corpus
}

func TestContextAdapter_EndToEnd(t *testing.T) {
	cli, corpus := newContextTestCLI(t)
	a := &contextPluginAdapter{cli: cli}

	out, err := a.Create("react-docs", "knowledge", []string{corpus}, "react docs", false)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !strings.Contains(out, "react-docs") || !strings.Contains(strings.ToLower(out), "created") {
		t.Errorf("Create summary = %q", out)
	}

	if list, _ := a.List(); !strings.Contains(list, "react-docs") {
		t.Errorf("List must show the created context: %q", list)
	}
	if st, _ := a.Status(); !strings.Contains(strings.ToLower(st), "nothing") {
		t.Errorf("Status before attach should be empty: %q", st)
	}

	out, err = a.Attach("react-docs", 0, 0)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if !strings.Contains(out, "Attached") || !strings.Contains(out, "react-docs") {
		t.Errorf("Attach summary = %q", out)
	}
	// Knowledge mode + Null embeddings → keyless BM25 retrieval.
	if !strings.Contains(out, "BM25") {
		t.Errorf("Attach should report keyless BM25 with null embeddings: %q", out)
	}

	if st, _ := a.Status(); !strings.Contains(st, "react-docs") {
		t.Errorf("Status after attach must list it: %q", st)
	}
	if list, _ := a.List(); !strings.Contains(list, "* react-docs") {
		t.Errorf("List must mark the attached context with *: %q", list)
	}

	if out, err = a.Detach("react-docs"); err != nil || !strings.Contains(out, "Detached") {
		t.Fatalf("Detach = %q, err %v", out, err)
	}
	if st, _ := a.Status(); !strings.Contains(strings.ToLower(st), "nothing") {
		t.Errorf("Status after detach should be empty: %q", st)
	}

	if out, err = a.Delete("react-docs"); err != nil || !strings.Contains(out, "Deleted") {
		t.Fatalf("Delete = %q, err %v", out, err)
	}
	if list, _ := a.List(); strings.Contains(list, "react-docs") {
		t.Errorf("deleted context must be gone from List: %q", list)
	}
}

func TestContextAdapter_FullParity(t *testing.T) {
	cli, corpus := newContextTestCLI(t)
	a := &contextPluginAdapter{cli: cli}

	if _, err := a.Create("docs-a", "knowledge", []string{corpus}, "a", false); err != nil {
		t.Fatalf("Create: %v", err)
	}

	show, err := a.Show("docs-a")
	if err != nil || !strings.Contains(show, "docs-a") || !strings.Contains(show, "mode: knowledge") {
		t.Fatalf("Show = %q, err %v", show, err)
	}
	insp, err := a.Inspect("docs-a", 0)
	if err != nil || !strings.Contains(insp, "sources:") {
		t.Fatalf("Inspect = %q, err %v", insp, err)
	}
	mt, err := a.Metrics()
	if err != nil || !strings.Contains(strings.ToLower(mt), "context store") {
		t.Fatalf("Metrics = %q, err %v", mt, err)
	}

	upd, err := a.Update("docs-a", []string{corpus}, "knowledge", "new desc", []string{"tag1"})
	if err != nil || !strings.Contains(upd, "docs-a") {
		t.Fatalf("Update = %q, err %v", upd, err)
	}

	exportPath := filepath.Join(t.TempDir(), "docs-a.json")
	if _, err := a.Export("docs-a", exportPath); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if _, err := os.Stat(exportPath); err != nil {
		t.Fatalf("export file missing: %v", err)
	}
	if imp, err := a.Import(exportPath); err != nil || !strings.Contains(imp, "Imported") {
		t.Fatalf("Import = %q, err %v", imp, err)
	}

	if _, err := a.Create("docs-b", "knowledge", []string{corpus}, "b", false); err != nil {
		t.Fatalf("Create docs-b: %v", err)
	}
	if mg, err := a.Merge("docs-all", []string{"docs-a", "docs-b"}, "merged"); err != nil || !strings.Contains(mg, "docs-all") {
		t.Fatalf("Merge = %q, err %v", mg, err)
	}

	// Validation paths.
	if _, err := a.Show("nope"); err == nil {
		t.Error("Show of a missing context must error")
	}
	if _, err := a.Update("nope", []string{corpus}, "", "", nil); err == nil {
		t.Error("Update of a missing context must error")
	}
	if _, err := a.Merge("x", []string{"docs-a", "nope"}, ""); err == nil {
		t.Error("Merge with a missing source must error")
	}
	if _, err := a.Export("nope", exportPath); err == nil {
		t.Error("Export of a missing context must error")
	}
}

func TestContextAdapter_Validation(t *testing.T) {
	cli, corpus := newContextTestCLI(t)
	a := &contextPluginAdapter{cli: cli}

	if _, err := a.Create("bad", "bogus-mode", []string{corpus}, "", false); err == nil {
		t.Error("invalid mode must error")
	}
	if _, err := a.Attach("nonexistent", 0, 0); err == nil {
		t.Error("attaching a missing context must error")
	}
	if _, err := a.Detach("nonexistent"); err == nil {
		t.Error("detaching a missing context must error")
	}
	if _, err := a.Delete("nonexistent"); err == nil {
		t.Error("deleting a missing context must error")
	}
}

func TestContextAdapter_WithoutManager(t *testing.T) {
	a := &contextPluginAdapter{cli: &ChatCLI{}}
	if _, err := a.Create("x", "knowledge", []string{"y"}, "", false); err == nil {
		t.Error("Create without a manager must error")
	}
	if _, err := a.Attach("x", 0, 0); err == nil {
		t.Error("Attach without a manager must error")
	}
	if _, err := a.List(); err == nil {
		t.Error("List without a manager must error")
	}
	if _, err := a.Status(); err == nil {
		t.Error("Status without a manager must error")
	}
}

func TestContextPipelineHint(t *testing.T) {
	h := contextPipelineHint()
	for _, want := range []string{"@websearch", "@docs-flatten", "@context create", "@context attach", "@knowledge"} {
		if !strings.Contains(h, want) {
			t.Errorf("pipeline hint missing %q:\n%s", want, h)
		}
	}
}
