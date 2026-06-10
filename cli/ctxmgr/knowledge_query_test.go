/*
 * ChatCLI - Knowledge query surface tests (@knowledge tool backend).
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package ctxmgr

import (
	"context"
	"strings"
	"testing"

	"github.com/diillson/chatcli/llm/embedding"
)

// newKnowledgeManager builds a manager with one knowledge base attached to
// session "sess" plus one non-knowledge context that must stay invisible.
func newKnowledgeManager(t *testing.T) (*Manager, *FileContext) {
	t.Helper()
	m := newTestManager(t)
	m.AttachEmbeddingProvider(embedding.NewNull())

	kb := knowledgeTestContext()
	m.contexts[kb.ID] = kb
	if err := m.AttachContext("sess", kb.ID, 1); err != nil {
		t.Fatal(err)
	}
	full := addTestContext(t, m, "plain-ctx", sampleFiles(), ModeFull, nil)
	if err := m.AttachContext("sess", full.ID, 2); err != nil {
		t.Fatal(err)
	}
	return m, kb
}

func TestAttachedKnowledge_FiltersByMode(t *testing.T) {
	m, kb := newKnowledgeManager(t)
	kbs := m.AttachedKnowledge("sess")
	if len(kbs) != 1 || kbs[0].ID != kb.ID {
		t.Fatalf("AttachedKnowledge = %v, want only the knowledge context", kbs)
	}
	if got := m.AttachedKnowledge("other-session"); len(got) != 0 {
		t.Fatal("unattached session must see no knowledge bases")
	}
}

func TestKnowledgeSearch_KeylessAndScoped(t *testing.T) {
	m, _ := newKnowledgeManager(t)

	hits, err := m.KnowledgeSearch(context.Background(), "sess", "", "homebrew install", 5)
	if err != nil {
		t.Fatalf("KnowledgeSearch: %v", err)
	}
	if len(hits) == 0 || hits[0].ContextName != "chatcli-docs" {
		t.Fatalf("hits = %v, want tagged passages from chatcli-docs", hits)
	}
	if !strings.Contains(hits[0].Seg.Content, "brew install") {
		t.Errorf("top hit content = %q", hits[0].Seg.Content)
	}

	if _, err := m.KnowledgeSearch(context.Background(), "sess", "nope-kb", "x", 5); err == nil {
		t.Fatal("unknown kb name must error with the attached list")
	}
	if _, err := m.KnowledgeSearch(context.Background(), "empty-session", "", "x", 5); err == nil {
		t.Fatal("session without knowledge bases must get an actionable error")
	}
}

func TestKnowledgeDocument_JoinsChunksAndPaginates(t *testing.T) {
	m, _ := newKnowledgeManager(t)

	page, total, next, err := m.KnowledgeDocument("sess", "", "guide/install.md", 0)
	if err != nil {
		t.Fatalf("KnowledgeDocument: %v", err)
	}
	if next != 0 || total != len(page) {
		t.Fatalf("small doc must fit one page: total=%d next=%d", total, next)
	}
	for _, want := range []string{"# Install", "## Homebrew"} {
		if !strings.Contains(page, want) {
			t.Errorf("document missing chunk content %q", want)
		}
	}

	if _, _, _, err := m.KnowledgeDocument("sess", "", "guide/missing.md", 0); err == nil {
		t.Fatal("unknown source must error")
	}
	if _, _, _, err := m.KnowledgeDocument("sess", "", "guide/install.md", 10_000); err == nil {
		t.Fatal("offset beyond end must error")
	}
}

func TestKnowledgeTOC_PrefixFilter(t *testing.T) {
	m, _ := newKnowledgeManager(t)

	toc, err := m.KnowledgeTOC("sess", "", "")
	if err != nil {
		t.Fatalf("KnowledgeTOC: %v", err)
	}
	if !strings.Contains(toc, "guide/install.md") || !strings.Contains(toc, "api/auth.md") {
		t.Errorf("toc incomplete:\n%s", toc)
	}

	scoped, err := m.KnowledgeTOC("sess", "", "api/")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(scoped, "guide/install.md") || !strings.Contains(scoped, "api/auth.md") {
		t.Errorf("prefix filter failed:\n%s", scoped)
	}
}

func TestFormatKnowledgeHits(t *testing.T) {
	if out := FormatKnowledgeHits("q", nil); !strings.Contains(out, "No passages matched") {
		t.Errorf("empty hits message = %q", out)
	}
	hits := []KnowledgeHit{{
		ContextName: "docs",
		Seg:         Segment{FilePath: "a.md#0001", StartLine: 1, EndLine: 3, Content: "body"},
	}}
	out := FormatKnowledgeHits("install", hits)
	for _, want := range []string{"docs :: a.md#0001", "body", `"cmd":"get"`} {
		if !strings.Contains(out, want) {
			t.Errorf("formatted hits missing %q:\n%s", want, out)
		}
	}
}
