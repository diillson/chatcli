/*
 * ChatCLI - Tests for the document graph of a context (docgraph.go)
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package ctxmgr

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/utils"
)

func docFixture() *FileContext {
	return &FileContext{
		ID: "c1", Name: "handbook", Mode: ModeKnowledge, UpdatedAt: time.Unix(1, 0),
		Files: []utils.FileInfo{
			// docs-flatten chunks of one document fold into it.
			{Path: "docs/guide/setup.md#0001", Type: "md", Content: "---\ntitle: Getting set up\n---\n\nInstall it. Then read [gateway](./gateway.md#tls \"TLS\").\n"},
			{Path: "docs/guide/setup.md#0002", Type: "md", Content: "More: [home](../README.md), [site](/guide/gateway), [ext](https://x.dev/a.md), [mail](mailto:a@b), [anchor](#top)."},
			{Path: "docs/guide/gateway.md#0001", Type: "md", Content: "```\n# not a title\n```\n# Ingest gateway\n\nTerminates mTLS. See [[Getting set up]], [[setup|the setup]] and [[Orbital]].\n"},
			{Path: "docs/README.md#0001", Type: "md", Content: "# Handbook\n\n<img src=x>\n| table |\nWelcome to the handbook. [guide](guide/)\n"},
			{Path: "docs/guide/index.md#0001", Type: "md", Content: "# Guide index\n\nStart at [[gateway]].\n"},
		},
	}
}

func TestBuildDocuments_FoldsChunksAndExtractsHeadlines(t *testing.T) {
	docs := BuildDocuments(docFixture())
	if len(docs) != 4 {
		t.Fatalf("documents = %d, want 4: %+v", len(docs), docs)
	}
	by := map[string]Document{}
	for _, d := range docs {
		by[d.Source] = d
	}
	setup := by["docs/guide/setup.md"]
	if setup.Chunks != 2 || setup.Title != "Getting set up" || setup.Summary != "Install it. Then read [gateway](./gateway.md#tls \"TLS\")." {
		t.Fatalf("setup = %+v", setup)
	}
	if g := by["docs/guide/gateway.md"]; g.Title != "Ingest gateway" {
		t.Fatalf("code-fenced heading taken as title: %+v", g)
	}
	if r := by["docs/README.md"]; r.Summary != "Welcome to the handbook. [guide](guide/)" {
		t.Fatalf("html/table lines leaked into summary: %q", r.Summary)
	}
}

func TestBuildDocuments_ResolvesMarkdownAndWikilinks(t *testing.T) {
	by := map[string]Document{}
	for _, d := range BuildDocuments(docFixture()) {
		by[d.Source] = d
	}
	wantSetup := []string{"docs/README.md", "docs/guide/gateway.md"}
	if got := by["docs/guide/setup.md"].Links; !reflect.DeepEqual(got, wantSetup) {
		t.Fatalf("setup links = %q, want %q", got, wantSetup)
	}
	gw := by["docs/guide/gateway.md"]
	if !reflect.DeepEqual(gw.Links, []string{"docs/guide/setup.md"}) {
		t.Fatalf("gateway links = %q (title + file-name wikilinks must resolve in-context)", gw.Links)
	}
	if !reflect.DeepEqual(gw.Wikilinks, []string{"Orbital"}) {
		t.Fatalf("unresolved wikilinks = %q, want [Orbital] for vault-wide resolution", gw.Wikilinks)
	}
	if got := by["docs/README.md"].Links; !reflect.DeepEqual(got, []string{"docs/guide/index.md"}) {
		t.Fatalf("directory link must resolve to its index page: %q", got)
	}
}

func TestBuildDocuments_WindowsPaths(t *testing.T) {
	fc := &FileContext{ID: "w", Files: []utils.FileInfo{
		{Path: `C:\repo\docs\a.md`, Content: "# A\n\nsee [b](b.md)"},
		{Path: `C:\repo\docs\b.md`, Content: "# B\n\nback to [[A]]"},
	}}
	by := map[string]Document{}
	for _, d := range BuildDocuments(fc) {
		by[d.Source] = d
	}
	if got := by[`C:\repo\docs\a.md`].Links; len(got) != 1 || got[0] != `C:\repo\docs\b.md` {
		t.Fatalf("windows relative link = %q", got)
	}
	if got := by[`C:\repo\docs\b.md`].Links; len(got) != 1 || got[0] != `C:\repo\docs\a.md` {
		t.Fatalf("windows wikilink = %q", got)
	}
}

func TestBuildDocuments_CapAndEmpty(t *testing.T) {
	if BuildDocuments(nil) != nil || BuildDocuments(&FileContext{}) != nil {
		t.Fatal("empty context must yield nil")
	}
	fc := &FileContext{ID: "big"}
	for i := 0; i < maxDocumentsPerContext+5; i++ {
		fc.Files = append(fc.Files, utils.FileInfo{Path: fmt.Sprintf("d/%05d.md", i), Content: "x"})
	}
	docs := BuildDocuments(fc)
	if len(docs) != maxDocumentsPerContext || !docs[len(docs)-1].Truncated {
		t.Fatalf("cap not applied: %d docs", len(docs))
	}
}

func TestDocuments_MemoizedPerRevisionAndDroppedOnDelete(t *testing.T) {
	m := &Manager{contexts: map[string]*FileContext{}}
	fc := docFixture()
	m.contexts[fc.ID] = fc
	a := m.Documents(fc.ID)
	b := m.Documents(fc.ID)
	if len(a) == 0 || &a[0] != &b[0] {
		t.Fatal("documents not memoized")
	}
	fc.UpdatedAt = time.Unix(2, 0) // new revision
	fc.Files = fc.Files[:1]
	if c := m.Documents(fc.ID); len(c) != 1 {
		t.Fatalf("stale memo served after a revision change: %d docs", len(c))
	}
	m.dropDocuments(fc.ID)
	if _, ok := m.docsCache[fc.ID]; ok {
		t.Fatal("memo survived delete")
	}
	if m.Documents("unknown") != nil {
		t.Fatal("unknown context must yield nil")
	}
}

func TestDocumentLinksByName(t *testing.T) {
	m := &Manager{contexts: map[string]*FileContext{}}
	fc := docFixture()
	m.contexts[fc.ID] = fc
	out, in := m.DocumentLinksByName("HANDBOOK", "docs/guide/gateway.md")
	if !reflect.DeepEqual(out, []string{"docs/guide/setup.md"}) {
		t.Fatalf("out = %q", out)
	}
	if !reflect.DeepEqual(in, []string{"docs/guide/index.md", "docs/guide/setup.md"}) {
		t.Fatalf("in = %q", in)
	}
	if o, i := m.DocumentLinksByName("nope", "x"); o != nil || i != nil {
		t.Fatal("unknown context must yield nil")
	}
}

func attachedFixture() *Manager {
	m := &Manager{contexts: map[string]*FileContext{}, attachedContexts: map[string][]AttachedContext{}}
	fc := docFixture()
	m.contexts[fc.ID] = fc
	m.attachedContexts["s1"] = []AttachedContext{{ContextID: fc.ID}}
	return m
}

func TestKnowledgeLinks(t *testing.T) {
	m := attachedFixture()
	out, err := m.KnowledgeLinks("s1", "", "docs/guide/gateway.md#0001")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`handbook :: docs/guide/gateway.md — "Ingest gateway"`,
		"→ links to (1):", `docs/guide/setup.md — "Getting set up"`,
		"← linked from (2):", "docs/guide/index.md",
		"[[wikilinks]] to notes outside this base (see @memory neighbors): Orbital",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if _, err := m.KnowledgeLinks("s1", "", "nope.md"); err == nil {
		t.Fatal("unknown document accepted")
	}
	if _, err := m.KnowledgeLinks("s1", "", " "); err == nil {
		t.Fatal("empty source accepted")
	}
	if _, err := m.KnowledgeLinks("other-session", "", "docs/guide/gateway.md"); err == nil {
		t.Fatal("unattached session reached the base")
	}
}

func TestKnowledgeLinkHints(t *testing.T) {
	m := attachedFixture()
	hits := []KnowledgeHit{{ContextName: "handbook", Seg: Segment{FilePath: "docs/guide/gateway.md#0001"}}}
	out := m.KnowledgeLinkHints(hits)
	if !strings.Contains(out, "handbook :: docs/guide/setup.md (linked from docs/guide/gateway.md)") ||
		!strings.Contains(out, "docs/guide/index.md (links to docs/guide/gateway.md)") {
		t.Fatalf("hints = %q", out)
	}
	// A linked document already among the hits is not repeated.
	hits = append(hits, KnowledgeHit{ContextName: "handbook", Seg: Segment{FilePath: "docs/guide/setup.md#0002"}})
	if strings.Contains(m.KnowledgeLinkHints(hits), ":: docs/guide/setup.md (") {
		t.Fatal("hit repeated as a hint")
	}
	if m.KnowledgeLinkHints(nil) != "" {
		t.Fatal("no hits must yield no hints")
	}
}

func TestBuildDocuments_IgnoresLinksInCode(t *testing.T) {
	fc := &FileContext{ID: "c", Files: []utils.FileInfo{
		{Path: "a.md", Content: "# A\n\n```\nsee [b](b.md) and [[B]]\n```\nprose [[C]]"},
		{Path: "b.md", Content: "# B"},
		{Path: "c.md", Content: "# C"},
		{Path: "run.sh", Content: "if [[ -f b ]]; then x=$(f](b.md)); fi"},
	}}
	by := map[string]Document{}
	for _, d := range BuildDocuments(fc) {
		by[d.Source] = d
	}
	if got := by["a.md"].Links; !reflect.DeepEqual(got, []string{"c.md"}) {
		t.Fatalf("links inside a fence counted: %q", got)
	}
	if len(by["run.sh"].Links) != 0 || len(by["run.sh"].Wikilinks) != 0 {
		t.Fatalf("code file produced links: %+v", by["run.sh"])
	}
}

func TestDocuments_RaceFreeWithInPlaceUpdates(t *testing.T) {
	m := &Manager{contexts: map[string]*FileContext{}}
	fc := docFixture()
	m.contexts[fc.ID] = fc
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { // the vault syncer / graph rebuild
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = m.Documents(fc.ID)
			_ = m.DocumentContent(fc.ID, "docs/guide/gateway.md")
			_ = m.SnapshotContexts()
			_ = m.HasDocument(fc.ID, "docs/README.md")
		}
	}()
	for i := 0; i < 200; i++ { // /context update / refresh
		m.mu.Lock()
		files := append([]utils.FileInfo(nil), fc.Files...)
		fc.Files = files
		fc.UpdatedAt = time.Unix(int64(i+10), 0)
		fc.Name = fmt.Sprintf("handbook-%d", i)
		if fc.Metadata == nil {
			fc.Metadata = map[string]string{}
		}
		fc.Metadata["kb.seal"] = fmt.Sprint(i) // refresh writes seals in place
		fc.FileStamps = map[string]FileStamp{"x": {}}
		m.mu.Unlock()
	}
	close(stop)
	<-done
}
