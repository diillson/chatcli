/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeMixedRepo builds a small repo mixing Terraform, GitOps YAML, shell, Go
// and noise (a lockfile, a binary, a vendored dir) for the code-ingestion e2e.
func writeMixedRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"infra/eks.tf": `resource "aws_eks_cluster" "main" {
  name = "prod"
}
`,
		"argo/rollout.yaml": `apiVersion: argoproj.io/v1alpha1
kind: Rollout
metadata:
  name: checkout-api
`,
		"scripts/deploy.sh": `#!/usr/bin/env bash
deploy() {
  kubectl apply -f argo/
}
`,
		"src/handler.go": `package svc

func HandleCheckout(id string) error {
	return nil
}
`,
		"README.md":               "# Service\nMixed repo.",
		"go.sum":                  "should be skipped",
		"assets/logo.png":         "\x89PNG\r\n\x1a\n binary",
		"vendor/dep/dep.go":       "package dep\nfunc Vendored() {}",
		"node_modules/x/index.js": "module.exports = {}",
	}
	for rel, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestDocsFlattenCodeKind_MixedRepo(t *testing.T) {
	dir := writeMixedRepo(t)
	p := NewBuiltinDocsFlattenPlugin()
	out, err := p.Execute(context.Background(),
		[]string{`{"root":"` + filepath.ToSlash(dir) + `","format":"jsonl","kind":"code"}`})
	if err != nil {
		t.Fatal(err)
	}

	bySource := map[string]docsFlattenChunk{}
	titles := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var c docsFlattenChunk
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			t.Fatalf("invalid JSONL %q: %v", line, err)
		}
		bySource[c.Source] = c
		if c.Title != "" {
			titles[c.Title] = true
		}
	}

	// Every code/infra source is ingested...
	for _, want := range []string{"infra/eks.tf", "argo/rollout.yaml", "scripts/deploy.sh", "src/handler.go", "README.md"} {
		if _, ok := bySource[want]; !ok {
			t.Errorf("expected %q ingested; sources=%v", want, sourceKeys(bySource))
		}
	}
	// ...and noise is skipped (lockfile, binary, vendored, node_modules).
	for _, bad := range []string{"go.sum", "assets/logo.png", "vendor/dep/dep.go", "node_modules/x/index.js"} {
		if _, ok := bySource[bad]; ok {
			t.Errorf("%q should have been skipped", bad)
		}
	}
	// Structure-aware titles per flavor.
	for _, want := range []string{"aws_eks_cluster.main", "Rollout/checkout-api", "deploy", "HandleCheckout"} {
		if !titles[want] {
			t.Errorf("missing structural title %q; got %v", want, titleKeys(titles))
		}
	}
}

// With the default kind=docs, the same repo yields only the Markdown file —
// proving code ingestion is strictly opt-in (backward compatible).
func TestDocsFlattenDefaultKind_DocsOnly(t *testing.T) {
	dir := writeMixedRepo(t)
	p := NewBuiltinDocsFlattenPlugin()
	out, err := p.Execute(context.Background(),
		[]string{`{"root":"` + filepath.ToSlash(dir) + `","format":"jsonl"}`})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 {
		t.Fatalf("default kind=docs should yield only the .md file, got %d:\n%s", len(lines), out)
	}
	var c docsFlattenChunk
	if err := json.Unmarshal([]byte(lines[0]), &c); err != nil {
		t.Fatal(err)
	}
	if c.Source != "README.md" {
		t.Errorf("expected README.md, got %q", c.Source)
	}
}

// TestDocsFlattenDocsOnCodeRepo_HintsKindCode is the self-correction contract:
// running the default (docs) on a code/infra repo with no Markdown must return
// an actionable hint pointing at kind=code, so the agent retries correctly in
// one turn instead of hitting a dead end.
func TestDocsFlattenDocsOnCodeRepo_HintsKindCode(t *testing.T) {
	dir := t.TempDir()
	for rel, content := range map[string]string{
		"main.go":    "package main\nfunc main() {}",
		"infra/x.tf": `resource "null_resource" "a" {}`,
	} {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out, err := NewBuiltinDocsFlattenPlugin().Execute(context.Background(),
		[]string{`{"root":"` + filepath.ToSlash(dir) + `","format":"jsonl"}`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "kind=code") {
		t.Errorf("docs-on-code-repo should hint kind=code, got: %q", out)
	}
}

func TestDocsFlattenInvalidKind(t *testing.T) {
	dir := writeMixedRepo(t)
	p := NewBuiltinDocsFlattenPlugin()
	_, err := p.Execute(context.Background(),
		[]string{`{"root":"` + filepath.ToSlash(dir) + `","kind":"banana"}`})
	if err == nil || !strings.Contains(err.Error(), "invalid kind") {
		t.Errorf("expected invalid kind error, got %v", err)
	}
}

func sourceKeys(m map[string]docsFlattenChunk) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func titleKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
