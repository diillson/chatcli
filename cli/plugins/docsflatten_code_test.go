/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"strings"
	"testing"
)

func TestDocsFlattenFlavor(t *testing.T) {
	cases := map[string]codeFlavor{
		"a/b/main.go":       flavorBrace,
		"svc/handler.ts":    flavorBrace,
		"infra/eks.tf":      flavorHCL,
		"vars.tfvars":       flavorHCL,
		"argo/rollout.yaml": flavorYAML,
		"deploy.sh":         flavorShell,
		"app.py":            flavorIndent,
		"lib.rb":            flavorIndent,
		"README.md":         flavorMarkdown,
		"page.mdx":          flavorMarkdown,
		"config.json":       flavorGeneric,
		"Dockerfile":        flavorGeneric,
	}
	for path, want := range cases {
		if got := docsFlattenFlavor(path); got != want {
			t.Errorf("docsFlattenFlavor(%q) = %d, want %d", path, got, want)
		}
	}
}

func TestDocsFlattenAcceptExt(t *testing.T) {
	type tc struct {
		name, kind string
		want       bool
	}
	cases := []tc{
		// docs kind: markdown only
		{"README.md", "docs", true},
		{"main.go", "docs", false},
		{"eks.tf", "docs", false},
		// code/auto kind: code + config + markdown
		{"main.go", "code", true},
		{"eks.tf", "auto", true},
		{"rollout.yaml", "code", true},
		{"deploy.sh", "code", true},
		{"config.json", "auto", true},
		{"Dockerfile", "code", true},
		{"Makefile", "code", true},
		{"README.md", "code", true},
		// always rejected: binaries, lockfiles, minified
		{"logo.png", "code", false},
		{"go.sum", "code", false},
		{"yarn.lock", "auto", false},
		{"bundle.min.js", "code", false},
		{"app.wasm", "auto", false},
	}
	for _, c := range cases {
		if got := docsFlattenAcceptExt(c.name, c.kind); got != c.want {
			t.Errorf("docsFlattenAcceptExt(%q, %q) = %v, want %v", c.name, c.kind, got, c.want)
		}
	}
}

func TestDocsFlattenSkipDir(t *testing.T) {
	for _, d := range []string{"vendor", "node_modules", ".terraform", "dist", "__pycache__"} {
		// .terraform is dot-skipped by the walker, but the rest must be caught here.
		if d == ".terraform" {
			continue
		}
		if !docsFlattenSkipDir(d) {
			t.Errorf("docsFlattenSkipDir(%q) should be true", d)
		}
	}
	if docsFlattenSkipDir("internal") {
		t.Error("internal must not be skipped")
	}
}

func TestChunkCode_HCLBlocksAndTitles(t *testing.T) {
	src := `variable "region" {
  default = "us-east-1"
}

resource "aws_eks_cluster" "main" {
  name = "prod"
  vpc_config {
    subnet_ids = var.subnets
  }
}

resource "aws_eks_node_group" "workers" {
  cluster_name = aws_eks_cluster.main.name
}
`
	chunks := chunkCode(flavorHCL, src, 16000)
	titles := collectTitles(chunks)
	for _, want := range []string{"variable.region", "aws_eks_cluster.main", "aws_eks_node_group.workers"} {
		if !titles[want] {
			t.Errorf("missing HCL title %q; got %v", want, keys(titles))
		}
	}
	// The nested vpc_config brace must NOT split the cluster resource.
	clusterChunk := findChunkWithTitle(chunks, "aws_eks_cluster.main")
	if clusterChunk == "" || !strings.Contains(clusterChunk, "subnet_ids") {
		t.Errorf("nested block split the resource: %q", clusterChunk)
	}
}

func TestChunkCode_BraceSymbols(t *testing.T) {
	src := `package main

import "fmt"

func Add(a, b int) int {
	return a + b
}

type Server struct {
	addr string
}

func (s *Server) Start() error {
	fmt.Println(s.addr)
	return nil
}
`
	chunks := chunkCode(flavorBrace, src, 16000)
	titles := collectTitles(chunks)
	for _, want := range []string{"Add", "Server", "Start"} {
		if !titles[want] {
			t.Errorf("missing brace title %q; got %v", want, keys(titles))
		}
	}
}

func TestChunkCode_YAMLDocsAndKindName(t *testing.T) {
	src := `apiVersion: argoproj.io/v1alpha1
kind: Rollout
metadata:
  name: checkout-api
spec:
  replicas: 3
---
apiVersion: v1
kind: Service
metadata:
  name: checkout-svc
`
	chunks := chunkCode(flavorYAML, src, 16000)
	titles := collectTitles(chunks)
	for _, want := range []string{"Rollout/checkout-api", "Service/checkout-svc"} {
		if !titles[want] {
			t.Errorf("missing YAML title %q; got %v", want, keys(titles))
		}
	}
	if len(chunks) != 2 {
		t.Errorf("expected 2 YAML docs, got %d", len(chunks))
	}
}

func TestChunkCode_ShellFunctions(t *testing.T) {
	src := `#!/usr/bin/env bash
set -euo pipefail

wait_for_ready() {
  until curl -sf localhost:8080/health; do
    sleep 1
  done
}

main() {
  wait_for_ready
}
main "$@"
`
	chunks := chunkCode(flavorShell, src, 16000)
	titles := collectTitles(chunks)
	for _, want := range []string{"wait_for_ready", "main"} {
		if !titles[want] {
			t.Errorf("missing shell title %q; got %v", want, keys(titles))
		}
	}
}

func TestChunkCode_IndentPython(t *testing.T) {
	src := `import os

def load(path):
    return open(path).read()

class Engine:
    def run(self):
        return 42
`
	chunks := chunkCode(flavorIndent, src, 16000)
	titles := collectTitles(chunks)
	for _, want := range []string{"load", "Engine"} {
		if !titles[want] {
			t.Errorf("missing python title %q; got %v", want, keys(titles))
		}
	}
}

func TestChunkCode_GenericWindowing(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 500; i++ {
		b.WriteString("key=value line that is reasonably long to force windowing\n")
	}
	chunks := chunkCode(flavorGeneric, b.String(), 1000)
	if len(chunks) < 2 {
		t.Fatalf("generic content should window into multiple chunks, got %d", len(chunks))
	}
	for _, c := range chunks {
		if len(c.Content) > 1100 { // maxChars + one line slack
			t.Errorf("generic chunk exceeded the window: %d chars", len(c.Content))
		}
	}
}

func TestChunkCode_BraceNoiseIgnored(t *testing.T) {
	// Braces inside strings/comments must not skew block boundaries.
	src := `func A() {
	s := "a { b } c"
	// closing } in a comment
	x := '{'
	_ = x
}

func B() {
	return
}
`
	chunks := chunkCode(flavorBrace, src, 16000)
	titles := collectTitles(chunks)
	if !titles["A"] || !titles["B"] {
		t.Errorf("brace-noise handling lost a symbol; titles=%v", keys(titles))
	}
}

func TestChunkCode_EmptyInput(t *testing.T) {
	if got := chunkCode(flavorBrace, "   \n\n", 16000); got != nil {
		t.Errorf("blank input should yield no chunks, got %d", len(got))
	}
}

// --- helpers ---

func collectTitles(chunks []flatChunk) map[string]bool {
	m := make(map[string]bool, len(chunks))
	for _, c := range chunks {
		if c.Title != "" {
			m[c.Title] = true
		}
	}
	return m
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func findChunkWithTitle(chunks []flatChunk, title string) string {
	for _, c := range chunks {
		if c.Title == title {
			return c.Content
		}
	}
	return ""
}
