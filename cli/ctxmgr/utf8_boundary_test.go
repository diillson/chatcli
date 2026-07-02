/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package ctxmgr

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/diillson/chatcli/utils"
)

// Knowledge corpora are routinely Portuguese; every byte-sliced boundary in
// this package must land on a rune start or the prompt receives invalid
// UTF-8. Three sites cut by byte position: the digest's title truncation
// (which lands in the CACHED system-prompt prefix), the search snippet, and
// the paged document read.

// TestDigestLineTruncatesTitleOnRuneBoundary: a 2-byte-rune title cut at the
// odd byte budget (digestMaxTitleChars-1 = 71) splits a rune.
func TestDigestLineTruncatesTitleOnRuneBoundary(t *testing.T) {
	s := digestSource{path: "docs/config.md", chunks: 3, title: strings.Repeat("ç", 100)}
	line := digestLine(s)
	if !utf8.ValidString(line) {
		t.Fatal("digest TOC line contains invalid UTF-8 (title cut mid-rune) — this text enters the cached prompt prefix")
	}
}

// TestSnippetCutsOnRuneBoundary: a space-free run of 3-byte runes offset by
// one ASCII byte guarantees the fallback byte cut at limit=360 lands mid-rune
// (rune starts sit at 1+3k; 360 is not of that form).
func TestSnippetCutsOnRuneBoundary(t *testing.T) {
	out := snippet("a"+strings.Repeat("→", 400), searchSnippetChars)
	if !utf8.ValidString(out) {
		t.Fatal("search snippet contains invalid UTF-8 (cut mid-rune)")
	}
}

// TestKnowledgeDocumentPagesOnRuneBoundaries: page cuts at byte offsets must
// snap to rune starts, and the returned nextOffset must reference the actual
// cut so the follow-up call resumes exactly where the page ended.
func TestKnowledgeDocumentPagesOnRuneBoundaries(t *testing.T) {
	m := newTestManager(t)
	// 1 ASCII byte then 3-byte runes: docPageChars lands mid-rune.
	content := "a" + strings.Repeat("→", docPageChars)
	fc := &FileContext{
		ID: "kb1", Name: "kb", Mode: ModeKnowledge,
		Files:     []utils.FileInfo{{Path: "docs/guia.md#0001", Content: content, Size: int64(len(content))}},
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	m.contexts[fc.ID] = fc
	if err := m.AttachContext("sess", fc.ID, 0); err != nil {
		t.Fatal(err)
	}

	// Walk the full pagination: every page must be valid UTF-8, every
	// nextOffset must equal the actual cut, and the pages must reassemble the
	// document byte-identically.
	var assembled strings.Builder
	offset, pages := 0, 0
	for {
		page, total, next, err := m.KnowledgeDocument("sess", "", "docs/guia.md", offset)
		if err != nil {
			t.Fatalf("page at offset %d: %v", offset, err)
		}
		if !utf8.ValidString(page) {
			t.Fatalf("page at offset %d contains invalid UTF-8 (cut mid-rune)", offset)
		}
		if next != 0 && next != offset+len(page) {
			t.Fatalf("nextOffset (%d) does not match the actual page end (%d) — the follow-up call would skip or repeat bytes", next, offset+len(page))
		}
		assembled.WriteString(page)
		pages++
		if next == 0 {
			if assembled.Len() != total {
				t.Fatalf("pages do not reassemble the document: %d != %d", assembled.Len(), total)
			}
			break
		}
		offset = next
	}
	if pages < 2 {
		t.Fatalf("fixture did not paginate (%d page)", pages)
	}
	if assembled.String() != content {
		t.Fatal("reassembled document differs from the source")
	}
}
