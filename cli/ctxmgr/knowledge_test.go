/*
 * ChatCLI - Knowledge mode tests: ingestion, lexical retrieval, hybrid fusion,
 * digest and manager wiring.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package ctxmgr

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diillson/chatcli/llm/embedding"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// writeCorpus writes a docs-flatten style JSONL file and returns its path.
func writeCorpus(t *testing.T, lines []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "docs.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestIngestKnowledgeJSONL(t *testing.T) {
	path := writeCorpus(t, []string{
		`{"id":"docs/intro.md#0001","source":"docs/intro.md","title":"Introduction","content":"# Introduction\nWelcome to the docs.","chunkSize":34,"repoUrl":"https://github.com/org/docs.git","commit":"abc123def4567890"}`,
		`{"id":"docs/intro.md#0002","source":"docs/intro.md","content":"## Install\nRun the installer."}`,
		`not json at all`,
		`{"id":"docs/api.md#0001","source":"docs/api.md","content":"# API\nEndpoints and auth."}`,
		`{"source":"docs/cli.md","content":"# CLI\nCommands reference."}`,      // id omitted: synthesized
		`{"id":"docs/empty.md#0001","source":"docs/empty.md","content":"   "}`, // empty content: skipped
		``,
	})
	files, meta, err := ingestKnowledgeJSONL(path, zap.NewNop())
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if len(files) != 4 {
		t.Fatalf("files = %d, want 4 (malformed/empty skipped)", len(files))
	}
	if files[0].Path != "docs/intro.md#0001" || files[0].Type != "md" {
		t.Errorf("first chunk = %s/%s, want docs/intro.md#0001/md", files[0].Path, files[0].Type)
	}
	if got := files[3].Path; !strings.HasPrefix(got, "docs/cli.md#") {
		t.Errorf("synthesized id = %q, want docs/cli.md#NNNN", got)
	}
	if meta[knowledgeMetaRepoURL] != "https://github.com/org/docs.git" {
		t.Errorf("repo metadata missing: %v", meta)
	}
	if meta[knowledgeMetaSources] != "3" {
		t.Errorf("sources = %s, want 3", meta[knowledgeMetaSources])
	}
}

func TestIngestKnowledgeJSONL_AllMalformedErrors(t *testing.T) {
	path := writeCorpus(t, []string{"junk", "{broken"})
	if _, _, err := ingestKnowledgeJSONL(path, nil); err == nil {
		t.Fatal("corpus without usable chunks must error")
	}
}

func TestChunkSourceAndJSONLPath(t *testing.T) {
	if got := chunkSource("docs/intro.md#0007"); got != "docs/intro.md" {
		t.Errorf("chunkSource = %q", got)
	}
	if got := chunkSource("plain/path.md"); got != "plain/path.md" {
		t.Errorf("chunkSource passthrough = %q", got)
	}
	if !isJSONLPath("/tmp/x.JSONL") || isJSONLPath("/tmp/x.json") {
		t.Error("isJSONLPath extension matching wrong")
	}
}

func TestLexicalIndex_BM25RanksExactTerms(t *testing.T) {
	fc := authDBCacheContext()
	segs := SegmentFiles(fc.Files, SegmentOptions{})
	lex := newLexicalIndex(segs)

	hits := lex.search("redis ttl eviction", 3)
	if len(hits) == 0 {
		t.Fatal("expected lexical hits")
	}
	if top := segs[hits[0].idx]; top.FilePath != "cache.go" {
		t.Errorf("top hit = %s, want cache.go", top.FilePath)
	}
	// Repeated query terms must not change the ranking via double-counted idf.
	dup := lex.search("redis redis redis ttl eviction", 3)
	if segs[dup[0].idx].FilePath != "cache.go" {
		t.Error("duplicate query terms changed the top hit")
	}
	if lex.search("", 3) != nil {
		t.Error("empty query must return nil")
	}
}

func TestRetrieveHybrid_KeylessLexicalFloor(t *testing.T) {
	// Null provider: hybrid must still answer via BM25 — the keyless contract.
	e := NewRetrievalEngine(embedding.NewNull(), t.TempDir(), zap.NewNop())
	fc := authDBCacheContext()

	segs, err := e.RetrieveHybrid(context.Background(), fc, "postgres schema tables", 2)
	if err != nil {
		t.Fatalf("RetrieveHybrid: %v", err)
	}
	if len(segs) == 0 || segs[0].FilePath != "db.go" {
		t.Fatalf("keyless hybrid top hit = %v, want db.go", segs)
	}
}

func TestRetrieveHybrid_BlendsVectorAndLexical(t *testing.T) {
	e := NewRetrievalEngine(conceptProvider{}, t.TempDir(), zap.NewNop())
	fc := authDBCacheContext()

	segs, err := e.RetrieveHybrid(context.Background(), fc, "how does login authentication work", 2)
	if err != nil {
		t.Fatalf("RetrieveHybrid: %v", err)
	}
	if len(segs) == 0 || segs[0].FilePath != "auth.go" {
		t.Fatalf("hybrid top hit = %v, want auth.go", segs)
	}
}

func TestEntryFor_CachesUntilContextChanges(t *testing.T) {
	e := NewRetrievalEngine(embedding.NewNull(), t.TempDir(), zap.NewNop())
	fc := authDBCacheContext()

	e1 := e.entryFor(fc)
	e2 := e.entryFor(fc)
	if e1 != e2 {
		t.Error("unchanged context must reuse the cached entry")
	}
	fc.TotalSize += 100 // fingerprint component changes
	if e3 := e.entryFor(fc); e3 == e1 {
		t.Error("changed context must rebuild the entry")
	}
}

func knowledgeTestContext() *FileContext {
	return &FileContext{
		ID:   "kb-test",
		Name: "chatcli-docs",
		Mode: ModeKnowledge,
		Files: []utils.FileInfo{
			{Path: "guide/install.md#0001", Type: "md", Content: "# Install\nHow to install the CLI.", Size: 33},
			{Path: "guide/install.md#0002", Type: "md", Content: "## Homebrew\nbrew install chatcli", Size: 32},
			{Path: "api/auth.md#0001", Type: "md", Content: "# Auth\noauth login bearer token flows", Size: 37},
		},
		FileCount: 3,
		TotalSize: 102,
		Metadata: map[string]string{
			knowledgeMetaRepoURL: "https://github.com/org/docs.git",
			knowledgeMetaCommit:  "abc123def4567890",
		},
	}
}

func TestBuildKnowledgeDigest_StableAndBounded(t *testing.T) {
	fc := knowledgeTestContext()
	d1 := BuildKnowledgeDigest(fc, 0)
	d2 := BuildKnowledgeDigest(fc, 0)
	if d1 != d2 {
		t.Fatal("digest must be byte-identical across calls (prompt cache)")
	}
	for _, want := range []string{
		"📚 KNOWLEDGE BASE: chatcli-docs",
		"2 document(s), 3 passage(s)",
		"guide/install.md (2 passages) — Install",
		"api/auth.md (1 passages) — Auth",
		"Origin: https://github.com/org/docs.git @ abc123def456",
		"index card only",
	} {
		if !strings.Contains(d1, want) {
			t.Errorf("digest missing %q\n---\n%s", want, d1)
		}
	}
	if strings.Contains(d1, "How to install") {
		t.Error("digest must not leak corpus content")
	}
}

func TestBuildKnowledgeDigest_BudgetTruncatesLoudly(t *testing.T) {
	fc := &FileContext{Name: "big", Mode: ModeKnowledge}
	for i := 0; i < 200; i++ {
		fc.Files = append(fc.Files, utils.FileInfo{
			Path:    "docs/page-" + itoa(i) + ".md#0001",
			Content: "# Page " + itoa(i),
			Size:    10,
		})
	}
	fc.FileCount = len(fc.Files)
	d := BuildKnowledgeDigest(fc, 1200)
	if len(d) > 1400 { // header overflows the toc budget slightly by design
		t.Fatalf("digest len = %d, want bounded near budget", len(d))
	}
	if !strings.Contains(d, "more document(s) not listed") {
		t.Error("truncation must be announced, not silent")
	}
}

func TestManager_KnowledgeAttachInjectsDigestAndRetrieves(t *testing.T) {
	m := newTestManager(t)
	m.AttachEmbeddingProvider(embedding.NewNull()) // keyless: engine must still exist
	fc := knowledgeTestContext()
	m.contexts[fc.ID] = fc
	if err := m.AttachContext("sess", fc.ID, 1); err != nil {
		t.Fatal(err)
	}

	msgs, err := m.BuildPromptMessages("sess", FormatOptions{Role: "system"})
	if err != nil || len(msgs) != 1 {
		t.Fatalf("BuildPromptMessages = %v msgs, err %v", len(msgs), err)
	}
	if !strings.Contains(msgs[0].Content, "📚 KNOWLEDGE BASE:") {
		t.Errorf("attach must inject the digest, got: %.80s", msgs[0].Content)
	}
	if strings.Contains(msgs[0].Content, "brew install") {
		t.Error("attach must NOT inject corpus content")
	}
	if msgs[0].Role != "system" {
		t.Errorf("role = %s, want system", msgs[0].Role)
	}

	retrieved, err := m.BuildRetrievedContextMessages(context.Background(), "sess", "how do I install with homebrew?")
	if err != nil {
		t.Fatal(err)
	}
	if len(retrieved) != 1 || !strings.Contains(retrieved[0].Content, "brew install chatcli") {
		t.Fatalf("keyless retrieval must surface the relevant passage, got %v", retrieved)
	}
	if !strings.Contains(retrieved[0].Content, "📚 KNOWLEDGE (retrieved): chatcli-docs") {
		t.Error("retrieved block must carry the knowledge header")
	}
}

func TestManager_CreateKnowledgeContextFromJSONL(t *testing.T) {
	m := newTestManager(t)
	path := writeCorpus(t, []string{
		`{"id":"a.md#0001","source":"a.md","content":"# Alpha\ncontent"}`,
		`{"id":"b.md#0001","source":"b.md","content":"# Beta\ncontent"}`,
	})
	fc, err := m.CreateContext("kb-docs", "docs corpus", []string{path}, ModeKnowledge, nil, false)
	if err != nil {
		t.Fatalf("CreateContext: %v", err)
	}
	if fc.FileCount != 2 || fc.Mode != ModeKnowledge {
		t.Fatalf("context = %d files mode %s, want 2/knowledge", fc.FileCount, fc.Mode)
	}
	if fc.Metadata[knowledgeMetaSources] != "2" {
		t.Errorf("sources metadata = %v", fc.Metadata)
	}
}

// countingProvider records every Embed batch size, delegating to conceptProvider.
type countingProvider struct {
	conceptProvider
	batches []int
}

func (c *countingProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	c.batches = append(c.batches, len(texts))
	return c.conceptProvider.Embed(ctx, texts)
}

// bigAuthCorpus builds a corpus with far more passages than the rerank pool.
func bigAuthCorpus(n int) *FileContext {
	fc := &FileContext{ID: "big-kb", Name: "big-kb", Mode: ModeKnowledge}
	for i := 0; i < n; i++ {
		fc.Files = append(fc.Files, utils.FileInfo{
			Path:    fmt.Sprintf("docs/page-%04d.md#0001", i),
			Type:    "md",
			Content: "oauth login bearer token authentication flows for page " + itoa(i),
			Size:    60,
		})
	}
	fc.FileCount = len(fc.Files)
	fc.TotalSize = int64(60 * n)
	return fc
}

// TestRetrieveHybrid_EmbedsOnlyTheCandidatePool pins the scalability contract:
// per-query embedding cost is bounded by the pool, never the corpus size.
func TestRetrieveHybrid_EmbedsOnlyTheCandidatePool(t *testing.T) {
	prov := &countingProvider{}
	e := NewRetrievalEngine(prov, t.TempDir(), zap.NewNop())
	fc := bigAuthCorpus(500) // 500 passages >> pool

	if _, err := e.RetrieveHybrid(context.Background(), fc, "oauth login authentication", 8); err != nil {
		t.Fatal(err)
	}
	var passagesEmbedded int
	for _, b := range prov.batches {
		if b > hybridMaxPool {
			t.Fatalf("one Embed batch carried %d texts — corpus leaked past the pool cap %d", b, hybridMaxPool)
		}
		if b > 1 {
			passagesEmbedded += b
		}
	}
	if passagesEmbedded == 0 || passagesEmbedded > hybridMaxPool {
		t.Fatalf("passages embedded = %d, want 1..%d (pool-bounded)", passagesEmbedded, hybridMaxPool)
	}

	// Same query again: candidates already cached — only the query re-embeds.
	prov.batches = nil
	if _, err := e.RetrieveHybrid(context.Background(), fc, "oauth login authentication", 8); err != nil {
		t.Fatal(err)
	}
	for _, b := range prov.batches {
		if b != 1 {
			t.Fatalf("repeat query must embed nothing but the query, got batch of %d", b)
		}
	}
}

// TestRetrieveHybrid_SemanticFallbackUsesCachedVectors covers queries with zero
// lexical overlap: cosine over already-cached vectors, no new passage embeds.
func TestRetrieveHybrid_SemanticFallbackUsesCachedVectors(t *testing.T) {
	prov := &countingProvider{}
	e := NewRetrievalEngine(prov, t.TempDir(), zap.NewNop())
	fc := authDBCacheContext()

	// Prime the vector cache through a lexical query.
	if _, err := e.RetrieveHybrid(context.Background(), fc, "oauth login token", 2); err != nil {
		t.Fatal(err)
	}
	prov.batches = nil

	// No corpus term matches; concept-space still maps to auth.
	segs, err := e.RetrieveHybrid(context.Background(), fc, "signin credential verification password", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) == 0 || segs[0].FilePath != "auth.go" {
		t.Fatalf("semantic fallback top hit = %v, want auth.go", segs)
	}
	for _, b := range prov.batches {
		if b != 1 {
			t.Fatalf("semantic fallback must only embed the query, got batch of %d", b)
		}
	}
}

// TestKnowledgeDigest_MemoizedPerRevision pins the per-turn digest cost.
func TestKnowledgeDigest_MemoizedPerRevision(t *testing.T) {
	m := newTestManager(t)
	fc := knowledgeTestContext()
	m.contexts[fc.ID] = fc

	first := m.KnowledgeDigest(fc)
	fc.Files = fc.Files[:1] // mutate WITHOUT touching the fingerprint
	if got := m.KnowledgeDigest(fc); got != first {
		t.Fatal("same revision must serve the memoized digest")
	}
	fc.TotalSize += 1 // revision changes → rebuild
	if got := m.KnowledgeDigest(fc); got == first {
		t.Fatal("new revision must rebuild the digest")
	}
}

// TestFormatKnowledgeHits_Snippets pins the compact search output: bounded
// passages with an elision marker, never the raw segment dump.
func TestFormatKnowledgeHits_Snippets(t *testing.T) {
	long := strings.Repeat("word ", 200) // ~1000 chars
	out := FormatKnowledgeHits("q", []KnowledgeHit{{
		ContextName: "docs",
		Seg:         Segment{FilePath: "a.md#0001", StartLine: 1, EndLine: 30, Content: long},
	}})
	if !strings.Contains(out, "[…]") || !strings.Contains(out, "snippets") {
		t.Errorf("long passages must be elided with a marker:\n%s", out)
	}
	if len(out) > 1200 {
		t.Errorf("single-hit result = %d bytes, want snippet-bounded", len(out))
	}
}
