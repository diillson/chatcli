/*
 * ChatCLI - Migration guarantee of the vault-model graph
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Upgrading to the typed-link builder must be invisible: same stores, no
 * conversion step, and nothing a user had before may disappear. These tests
 * build one rich fixture through BOTH builders — the frozen legacy oracle
 * (knowledge_graph_legacy_oracle_test.go) and the live one — and require the
 * new graph to be a superset: every legacy node with identical kind, title
 * and summary, every legacy edge with at least its legacy weight.
 */
package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/board"
	"github.com/diillson/chatcli/cli/ctxmgr"
	"github.com/diillson/chatcli/cli/workspace/memory"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/knowledge"
	"go.uber.org/zap"
)

// richGraphFixture populates every source the graph derives from.
func richGraphFixture(t *testing.T) *ChatCLI {
	t.Helper()
	cli := newTestCLIWithMemory(t)
	m := cli.memoryStore.Manager()

	m.Projects.Upsert(map[string]string{"name": "Orbital", "project_path": "/repo/orbital", "project_description": "satellite telemetry service", "project_technologies": "Go, gRPC"})
	m.Projects.Upsert(map[string]string{"name": "Ledgerline", "project_path": "/repo/ledgerline", "project_description": "billing pipeline"})

	m.Facts.AddFactWithSource("Orbital streams telemetry over gRPC with mTLS", "architecture", []string{"grpc", "security"}, "/repo/orbital")
	m.Facts.AddFactWithSource("Ledgerline retries webhooks with exponential backoff", "pattern", []string{"webhooks"}, "/repo/ledgerline")
	m.Facts.AddFactWithSource("prefers table-driven tests in [[Orbital]]", "preference", nil, "/repo/orbital")
	m.Facts.AddFact("the deploy window is Tuesday mornings", "general", []string{"ops"})
	// A wikilink whose name a tag and a topic share: the legacy resolver
	// picked the tag (first by id), the new one prefers the topic — both
	// edges must exist.
	m.Facts.AddFact("audit trail entries follow [[ops]] review rules", "pattern", []string{"audit"})
	m.Topics.Record([]string{"ops"})

	var telemetryFact string
	for _, f := range m.Facts.GetAll() {
		if f.SourceProject == "/repo/orbital" && f.Category == "architecture" {
			telemetryFact = f.ID
		}
	}
	if telemetryFact == "" {
		t.Fatal("fixture fact missing")
	}
	m.Topics.RecordWithSummary(map[string]string{"telemetry security": "mTLS between collectors and the ingest gateway"})
	m.Topics.LinkFact("telemetry security", telemetryFact)

	m.Profile.Update(map[string]string{"name": "Sample User", "role": "SRE", "skills": "Kubernetes, Go", "goals": "ship Orbital v2"})

	m.Episodes.Add(memory.Episode{Summary: "hardened the Orbital ingest gateway", Outcome: "merged", Project: "/repo/orbital", Date: time.Now()})

	sm := newTestSessionManager(t)
	cli.sessionManager = sm
	sd := &SessionData{Version: 2, ChatHistory: []models.Message{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "hello"}}}
	if err := sm.SaveSessionV2("orbital-gateway-review", sd); err != nil {
		t.Fatalf("save session: %v", err)
	}

	store := board.NewStore(filepath.Join(t.TempDir(), "board.json"))
	prev := boardStore
	boardStore = func() *board.Store { return store }
	t.Cleanup(func() { boardStore = prev })
	if _, err := store.Create("rotate Orbital certificates", "before the deploy window", "worker-a", board.ColDoing); err != nil {
		t.Fatalf("board: %v", err)
	}

	// A skill whose trigger names an existing topic (skill↔topic edge).
	projectDir := t.TempDir()
	skillsDir := filepath.Join(projectDir, ".agent", "skills")
	if err := os.MkdirAll(skillsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	skill := "---\nname: rotate-certs\ndescription: Rotate Orbital gateway certificates\ntriggers: [\"telemetry security\", \"certificates\"]\n---\nSteps.\n"
	if err := os.WriteFile(filepath.Join(skillsDir, "rotate-certs.md"), []byte(skill), 0o600); err != nil {
		t.Fatal(err)
	}
	cli.personaHandler = NewPersonaHandler(zap.NewNop())
	cli.personaHandler.GetManager().SetProjectDir(projectDir)
	_, _ = cli.personaHandler.GetManager().RefreshSkills()

	ch, err := NewContextHandler(zap.NewNop())
	if err != nil {
		t.Skipf("NewContextHandler unavailable in this environment: %v", err)
	}
	cli.contextHandler = ch
	docs := t.TempDir()
	writeDoc(t, docs, "guide/setup.md", "# Setup\n\nInstall the collector, then read [the gateway](gateway.md#tls).\n")
	writeDoc(t, docs, "guide/gateway.md", "# Ingest gateway\n\nTerminates mTLS. See [[Setup]] and [[Orbital]].\n")
	// A document titled like the "ops" tag and topic: it must never steal
	// the legacy [[ops]] target.
	writeDoc(t, docs, "guide/ops.md", "# ops\n\nRunbook index.\n")
	if _, err := ch.GetManager().CreateContext(context.Background(), "orbital-docs", "Orbital handbook", []string{docs}, ctxmgr.ModeKnowledge, []string{"handbook"}, true); err != nil {
		t.Fatalf("create knowledge context: %v", err)
	}
	if _, err := ch.GetManager().CreateContext(context.Background(), "orbital-src", "source notes", []string{docs}, ctxmgr.ModeFull, nil, true); err != nil {
		t.Fatalf("create full context: %v", err)
	}
	return cli
}

func writeDoc(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestVaultGraph_IsSupersetOfLegacyGraph(t *testing.T) {
	cli := richGraphFixture(t)
	legacy := cli.legacyBuildKnowledgeGraph()
	current := cli.buildKnowledgeGraph()

	if legacy.Len() < 10 || legacy.Edges() < 10 {
		t.Fatalf("fixture too thin to prove anything: %d nodes, %d edges", legacy.Len(), legacy.Edges())
	}
	for _, ln := range legacy.Nodes() {
		cn, ok := current.Node(ln.ID)
		if !ok {
			t.Errorf("legacy node %q lost", ln.ID)
			continue
		}
		if cn.Kind != ln.Kind || cn.Title != ln.Title || cn.Summary != ln.Summary || cn.Weight != ln.Weight {
			t.Errorf("legacy node %q changed:\n  was %+v\n  now %+v", ln.ID, *ln, *cn)
		}
	}
	curW := make(map[[2]string]float64)
	for _, e := range current.EdgeList() {
		curW[[2]string{e.A, e.B}] = e.Weight
	}
	for _, e := range legacy.EdgeList() {
		w, ok := curW[[2]string{e.A, e.B}]
		if !ok {
			t.Errorf("legacy edge %s — %s lost", e.A, e.B)
			continue
		}
		if w < e.Weight {
			t.Errorf("legacy edge %s — %s weakened: %v → %v", e.A, e.B, e.Weight, w)
		}
	}
	if current.LinkCount() == 0 {
		t.Fatal("typed links missing from the new graph")
	}
	// Every adjacency the new graph has is explained by at least one typed
	// link: no untyped edge sneaks in.
	for _, e := range current.EdgeList() {
		if len(current.LinksBetween(e.A, e.B)) == 0 {
			t.Errorf("edge %s — %s has no typed link", e.A, e.B)
		}
	}
}

func TestVaultGraph_StructuralWeightsUnchangedWithoutNewSignals(t *testing.T) {
	// With no prose that names another note, no documents and no explicit
	// links, the new builder must produce EXACTLY the legacy adjacency.
	cli := newTestCLIWithMemory(t)
	m := cli.memoryStore.Manager()
	m.Projects.Upsert(map[string]string{"name": "Quasar", "project_path": "/repo/quasar"})
	m.Facts.AddFactWithSource("builds use reproducible flags", "architecture", []string{"build"}, "/repo/quasar")
	m.Topics.Record([]string{"reproducibility"})
	for _, f := range m.Facts.GetAll() {
		m.Topics.LinkFact("reproducibility", f.ID)
	}
	m.Profile.Update(map[string]string{"skills": "Go"})

	legacy, current := cli.legacyBuildKnowledgeGraph(), cli.buildKnowledgeGraph()
	lb, _ := json.Marshal(legacy.EdgeList())
	cb, _ := json.Marshal(current.EdgeList())
	if string(lb) != string(cb) {
		t.Fatalf("adjacency drifted:\nlegacy  %s\ncurrent %s", lb, cb)
	}
}

func TestVaultGraph_V1CacheFileIsRebuiltTransparently(t *testing.T) {
	t.Setenv("CHATCLI_MEMORY_GRAPH", "")
	cli := newTestCLIWithMemory(t)
	m := cli.memoryStore.Manager()
	m.Projects.Upsert(map[string]string{"name": "Quasar", "project_path": "/repo/quasar"})
	m.Facts.AddFactWithSource("Quasar pins its toolchain", "architecture", nil, "/repo/quasar")

	// A graph.json written by a pre-vault build (schema 1, no typed links).
	v1 := `{"schema_version":1,"fingerprint":"x","saved_at":"2026-10-01T00:00:00Z","graph":{"nodes":[{"id":"fact:old","kind":"fact","title":"stale"}],"edges":[]}}`
	path := filepath.Join(m.MemoryDir(), "graph.json")
	if err := os.WriteFile(path, []byte(v1), 0o600); err != nil {
		t.Fatal(err)
	}
	cli.wireMemoryGraph()
	t.Cleanup(m.WaitGraphPersist)

	g := cli.knowledgeGraph()
	if _, stale := g.Node("fact:old"); stale {
		t.Fatal("v1 cache was adopted instead of rebuilt")
	}
	if _, ok := g.Node("project:quasar"); !ok {
		t.Fatal("rebuilt graph lost the live stores")
	}
	if len(g.Outlinks("project:quasar"))+len(g.Backlinks("project:quasar")) == 0 {
		t.Fatal("rebuilt graph carries no typed links")
	}
}

func TestVaultGraph_ContextDocumentsAndLinks(t *testing.T) {
	cli := richGraphFixture(t)
	g := cli.buildKnowledgeGraph()

	var kbID, ctxID string
	for _, n := range g.Nodes() {
		switch {
		case n.Kind == knowledge.KindKB && n.Title == "orbital-docs":
			kbID = n.ID
		case n.Kind == knowledge.KindContext && n.Title == "orbital-src":
			ctxID = n.ID
		}
	}
	if kbID == "" || ctxID == "" {
		t.Fatalf("context nodes missing: kb=%q ctx=%q", kbID, ctxID)
	}
	setup, gateway := findDoc(t, g, kbID, "Setup"), findDoc(t, g, kbID, "Ingest gateway")
	if !hasLink(g.Outlinks(setup), gateway, knowledge.RelLinksTo) {
		t.Errorf("markdown link setup→gateway missing: %+v", g.Outlinks(setup))
	}
	if !hasLink(g.Outlinks(gateway), setup, knowledge.RelLinksTo) {
		t.Errorf("[[Setup]] wikilink gateway→setup missing: %+v", g.Outlinks(gateway))
	}
	if !hasLink(g.Outlinks(gateway), "project:orbital", knowledge.RelLinksTo) {
		t.Errorf("vault-wide [[Orbital]] wikilink from a document missing: %+v", g.Outlinks(gateway))
	}
	if !hasLink(g.Outlinks(setup), kbID, knowledge.RelPartOf) {
		t.Errorf("doc part_of context missing: %+v", g.Outlinks(setup))
	}
	if !hasLink(g.Backlinks(setup), gateway, knowledge.RelLinksTo) {
		t.Errorf("backlink of setup missing: %+v", g.Backlinks(setup))
	}
	// Documents never crowd the per-turn card's hubs.
	if card := g.IndexCard(8); strings.Contains(card, "Setup") {
		t.Errorf("document leaked into card hubs: %s", card)
	}
}

func TestVaultGraph_MentionsAutolinkProse(t *testing.T) {
	cli := richGraphFixture(t)
	g := cli.buildKnowledgeGraph()
	var webhooks string
	for _, f := range cli.memoryStore.Manager().Facts.GetAll() {
		if f.Category == "pattern" {
			webhooks = "fact:" + f.ID
		}
	}
	// "Ledgerline retries webhooks…" has source project /repo/ledgerline,
	// so it is already in_project: the mention must NOT restate it.
	for _, l := range g.Outlinks(webhooks) {
		if l.To == "project:ledgerline" && l.Rel == knowledge.RelMentions {
			t.Fatal("mention restated an existing structural link")
		}
	}
	// The board card names Orbital in prose and has no other link to it.
	var card string
	for _, n := range g.Nodes() {
		if n.Kind == knowledge.KindCard {
			card = n.ID
		}
	}
	if !hasLink(g.Outlinks(card), "project:orbital", knowledge.RelMentions) {
		t.Fatalf("card prose mention of Orbital not autolinked: %+v", g.Outlinks(card))
	}
}

func TestVaultGraph_StoredLinksFollowSupersession(t *testing.T) {
	cli := newTestCLIWithMemory(t)
	m := cli.memoryStore.Manager()
	m.Projects.Upsert(map[string]string{"name": "Quasar", "project_path": "/repo/quasar"})
	m.Facts.AddFactWithMeta("the Quasar release cadence is weekly", "project", nil, "", 0.8, memory.ProvenanceUser)
	var oldID string
	for _, f := range m.Facts.GetAll() {
		oldID = f.ID
	}
	if ok, _ := m.Links.Add("fact:"+oldID, "project:quasar", "about", "user", ""); !ok {
		t.Fatal("link not stored")
	}
	// A restatement with the same subject supersedes the old fact.
	m.Facts.AddFactWithMeta("the Quasar release cadence is biweekly", "project", nil, "", 0.9, memory.ProvenanceUser)
	var newID string
	for _, f := range m.Facts.GetAll() {
		if f.ID != oldID {
			newID = f.ID
		}
	}
	if newID == "" || len(m.Facts.GetAll()) != 1 {
		t.Skip("supersession heuristics did not fire for this phrasing")
	}
	g := cli.buildKnowledgeGraph()
	if !hasLink(g.Outlinks("fact:"+newID), "project:quasar", knowledge.Rel("about")) {
		t.Fatalf("explicit link did not follow the superseding fact: %+v", g.Outlinks("fact:"+newID))
	}
}

func findDoc(t *testing.T, g *knowledge.Graph, ctxNode, title string) string {
	t.Helper()
	for _, l := range g.Backlinks(ctxNode) {
		if n, ok := g.Node(l.From); ok && n.Kind == knowledge.KindDoc && n.Title == title {
			return n.ID
		}
	}
	t.Fatalf("document %q of %s missing", title, ctxNode)
	return ""
}

func hasLink(ls []knowledge.Link, other string, rel knowledge.Rel) bool {
	for _, l := range ls {
		if (l.To == other || l.From == other) && l.Rel == rel {
			return true
		}
	}
	return false
}

// N1: the CACHED graph (the default path) and the vault must carry skills —
// the graph binds its stores at wiring time, so wiring before the persona
// handler exists would freeze a graph without them.
func TestVaultGraph_CachedGraphAndVaultCarrySkills(t *testing.T) {
	t.Setenv("CHATCLI_MEMORY_GRAPH", "")
	cli := richGraphFixture(t)
	cli.wireMemoryGraph()
	t.Cleanup(cli.memoryStore.Manager().WaitGraphPersist)
	g := cli.memoryStore.Manager().KnowledgeGraph()
	if g == nil {
		t.Fatal("cached graph not wired")
	}
	if _, ok := g.Node("skill:rotate-certs"); !ok {
		t.Fatalf("cached graph lost the skills: %v", g.CountByKind())
	}
	if !hasLink(g.Outlinks("skill:rotate-certs"), "topic:telemetry security", knowledge.RelTriggeredBy) {
		t.Fatalf("skill→topic edge missing: %+v", g.Outlinks("skill:rotate-certs"))
	}
	notes := cli.graphSources().buildVaultNotes(g)
	found := false
	for _, n := range notes {
		if strings.HasPrefix(n.Path, "Skills/") {
			found = true
		}
	}
	if !found {
		t.Fatal("vault has no Skills/ notes")
	}
}

// NewChatCLI must wire the graph after every source exists: guard the
// order structurally, since the full constructor needs a live provider.
func TestNewChatCLI_WiresGraphAfterPersona(t *testing.T) {
	src, err := os.ReadFile("cli.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	persona := strings.Index(body, "cli.personaHandler = NewPersonaHandler(logger)")
	wire := strings.Index(body, "cli.wireMemoryGraph()")
	vault := strings.Index(body, "cli.wireVault()")
	if persona < 0 || wire < 0 || vault < 0 {
		t.Fatal("constructor landmarks moved; update this guard")
	}
	if wire < persona || vault < persona {
		t.Fatal("the graph/vault are wired before the persona handler exists: skills would vanish from the cached graph")
	}
}
