/*
 * ChatCLI - @knowledge adapter tests.
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

// newKnowledgeTestCLI builds a minimal ChatCLI whose context manager lives
// under a throwaway HOME, with one knowledge base created from a docs-flatten
// JSONL corpus and attached to the default session.
func newKnowledgeTestCLI(t *testing.T) *ChatCLI {
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
		`{"id":"guide/install.md#0002","source":"guide/install.md","content":"## Homebrew\nbrew install chatcli"}`,
		`{"id":"api/auth.md#0001","source":"api/auth.md","content":"# Auth\noauth login bearer token flows"}`,
	}, "\n")
	if err := os.WriteFile(corpus, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	fc, err := handler.GetManager().CreateContext("chatcli-docs", "test corpus", []string{corpus}, "knowledge", nil, false)
	if err != nil {
		t.Fatalf("CreateContext: %v", err)
	}
	if err := handler.GetManager().AttachContext("default", fc.ID, 1); err != nil {
		t.Fatalf("AttachContext: %v", err)
	}
	return &ChatCLI{contextHandler: handler, logger: zap.NewNop()}
}

func TestKnowledgeAdapter_EndToEnd(t *testing.T) {
	cli := newKnowledgeTestCLI(t)
	a := &knowledgePluginAdapter{cli: cli}

	list, err := a.List()
	if err != nil || !strings.Contains(list, "chatcli-docs") {
		t.Fatalf("List = %q, err %v", list, err)
	}

	// topK above the cap must be clamped, not rejected.
	found, err := a.Search("homebrew install", "", 500)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !strings.Contains(found, "brew install chatcli") || !strings.Contains(found, "guide/install.md") {
		t.Errorf("Search must return the cited passage, got:\n%s", found)
	}

	doc, err := a.Get("guide/install.md", "", 0)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !strings.Contains(doc, "# Install") || !strings.Contains(doc, "## Homebrew") {
		t.Errorf("Get must join the document chunks, got:\n%s", doc)
	}
	if strings.Contains(doc, "document continues") {
		t.Error("small document must not advertise a continuation")
	}
	if _, err := a.Get("guide/missing.md", "", 0); err == nil {
		t.Error("unknown source must error")
	}

	toc, err := a.TOC("", "api/")
	if err != nil {
		t.Fatalf("TOC: %v", err)
	}
	if !strings.Contains(toc, "api/auth.md") || strings.Contains(toc, "guide/install.md") {
		t.Errorf("TOC prefix filter failed:\n%s", toc)
	}

	block := cli.knowledgeAgentBlock()
	if !strings.Contains(block, "📚 KNOWLEDGE BASE: chatcli-docs") || !strings.Contains(block, "@knowledge") {
		t.Errorf("agent block must carry the digest and the tool hint, got:\n%s", block)
	}
}

func TestKnowledgeAdapter_WithoutManager(t *testing.T) {
	cli := &ChatCLI{} // no contextHandler wired
	a := &knowledgePluginAdapter{cli: cli}

	if _, err := a.Search("x", "", 0); err == nil {
		t.Error("Search without a manager must error")
	}
	if _, err := a.Get("x", "", 0); err == nil {
		t.Error("Get without a manager must error")
	}
	if _, err := a.TOC("", ""); err == nil {
		t.Error("TOC without a manager must error")
	}
	if _, err := a.List(); err == nil {
		t.Error("List without a manager must error")
	}
	if cli.knowledgeAgentBlock() != "" {
		t.Error("agent block without a manager must be empty")
	}
}

func TestKnowledgeAdapter_SessionScoping(t *testing.T) {
	cli := newKnowledgeTestCLI(t)
	cli.currentSessionName = "another-session"
	a := &knowledgePluginAdapter{cli: cli}

	if _, err := a.Search("anything", "", 0); err == nil {
		t.Error("session without attached bases must get an actionable error")
	}
	if cli.knowledgeAgentBlock() != "" {
		t.Error("agent block must be empty for sessions without knowledge bases")
	}

	out, err := a.List()
	if err != nil || !strings.Contains(out, "No knowledge base attached") {
		t.Errorf("List = %q, err %v", out, err)
	}
}
