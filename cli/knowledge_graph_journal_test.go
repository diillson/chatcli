/*
 * ChatCLI - Tests for journal notes and store-backed link GC (knowledge_graph.go)
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/workspace/memory"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/knowledge"
	"go.uber.org/zap"
)

func TestJournalNodesInGraph(t *testing.T) {
	cli := newTestCLIWithMemory(t)
	m := cli.memoryStore.Manager()
	m.Projects.Upsert(map[string]string{"name": "Orbital", "project_path": "/repo/orbital"})
	if err := m.WriteDailyNote("Reviewed the Orbital ingest gateway with the team."); err != nil {
		t.Fatal(err)
	}
	m.Episodes.Add(memory.Episode{Summary: "shipped the gateway fix", Project: "/repo/orbital", Date: time.Now()})
	g := cli.buildKnowledgeGraph()
	id := "note:daily:" + time.Now().Format("2006-01-02")
	n, ok := g.Node(id)
	if !ok || n.Kind != knowledge.KindNote {
		t.Fatalf("daily note node missing: %v", g.CountByKind())
	}
	if r, ok := g.Resolve(time.Now().Format("2006-01-02")); !ok || r.ID != id {
		t.Fatal("daily note must resolve by its date ([[2026-10-24]])")
	}
	sameDay, mention := false, false
	for _, l := range g.Outlinks(id) {
		if l.Rel == knowledge.RelSameDay {
			sameDay = true
		}
		if l.Rel == knowledge.RelMentions && l.To == "project:orbital" {
			mention = true
		}
	}
	if !sameDay || !mention {
		t.Fatalf("daily note links = %+v", g.Outlinks(id))
	}
}

func TestIsoWeekStart(t *testing.T) {
	for label, want := range map[string]string{"2026-W01": "2025-12-29", "2026-W27": "2026-06-29", "2021-W53": "2022-01-03"} {
		got, ok := isoWeekStart(label)
		if !ok || got.Format("2006-01-02") != want {
			t.Errorf("isoWeekStart(%s) = %s, want %s", label, got.Format("2006-01-02"), want)
		}
	}
	for _, bad := range []string{"2026-27", "x-Wy", "2026-W54"} {
		if _, ok := isoWeekStart(bad); ok {
			t.Errorf("isoWeekStart(%q) accepted", bad)
		}
	}
}

// H1: a link to a note that only fell out of the graph's caps (the 50
// newest sessions) must never be collected.
func TestLinkGC_JudgesTheStoresNotTheCappedGraph(t *testing.T) {
	cli := newTestCLIWithMemory(t)
	m := cli.memoryStore.Manager()
	m.Projects.Upsert(map[string]string{"name": "Orbital", "project_path": "/repo/orbital"})
	sm := newTestSessionManager(t)
	cli.sessionManager = sm
	sd := func() *SessionData {
		return &SessionData{Version: 2, ChatHistory: []models.Message{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "ok"}}}
	}
	if err := sm.SaveSessionV2("aaa-oldest-review", sd()); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	_ = os.Chtimes(filepath.Join(sm.sessionsDir, "aaa-oldest-review.json"), old, old)
	for i := 0; i < graphMaxSessionNodes+3; i++ {
		if err := sm.SaveSessionV2(fmt.Sprintf("newer-%03d", i), sd()); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := cli.buildKnowledgeGraph().Node("session:aaa-oldest-review"); ok {
		t.Fatal("fixture: the oldest session should be beyond the graph cap")
	}
	if ok, _ := m.Links.Add("session:aaa-oldest-review", "project:orbital", "about", "user", ""); !ok {
		t.Fatal("add")
	}
	m.Facts.AddFact("the release cadence is weekly", "project", nil)
	var oldFact string
	for _, f := range m.Facts.GetAll() {
		oldFact = f.ID
	}
	_, _ = m.Links.Add("fact:"+oldFact, "project:orbital", "about", "user", "")
	_, _ = m.Links.Add("fact:gone-for-real", "project:orbital", "about", "user", "")
	cli.wireMemoryGraph()
	t.Cleanup(m.WaitGraphPersist)

	// Two passes far apart: the second is where a dangling link would go.
	m.GCLinks()
	backdateMissing(t, m)
	// A month later, a new process loads the aged store.
	m.Links = memory.NewLinkStore(m.MemoryDir(), nil)
	m.GCLinks()

	live := map[string]bool{}
	for _, l := range m.Links.All() {
		live[l.From] = true
	}
	if !live["session:aaa-oldest-review"] || !live["fact:"+oldFact] {
		t.Fatalf("GC dropped links whose endpoints exist: %v", live)
	}
	if live["fact:gone-for-real"] {
		t.Fatal("a link to a fact that no longer exists must be collected")
	}
}

// backdateMissing ages every MissingSince mark past the GC grace period,
// on disk, as a store a month older would read.
func backdateMissing(t *testing.T, m *memory.Manager) {
	t.Helper()
	path := filepath.Join(m.MemoryDir(), "links.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var links []memory.StoredLink
	if err := json.Unmarshal(data, &links); err != nil {
		t.Fatal(err)
	}
	for i := range links {
		if !links[i].MissingSince.IsZero() {
			links[i].MissingSince = time.Now().Add(-31 * 24 * time.Hour)
		}
	}
	out, _ := json.Marshal(links)
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(path, time.Now().Add(time.Second), time.Now().Add(time.Second))
}

// A context whose file is on disk but failed to load is not deleted: its
// explicit links must survive GC.
func TestLinkIndex_UnloadedContextIsNotDeleted(t *testing.T) {
	cli := newTestCLIWithMemory(t)
	ch, err := NewContextHandler(zap.NewNop())
	if err != nil {
		t.Skip(err)
	}
	cli.contextHandler = ch
	dir := ch.GetManager().Storage.GetStoragePath()
	if err := os.WriteFile(filepath.Join(dir, "ctx-unloadable.json"), []byte("{sealed or broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	exists := cli.graphSources().linkExistence()
	if !exists("kb:ctx-unloadable") || !exists("doc:ctx-unloadable:a.md") {
		t.Fatal("a context file on disk was taken for deleted")
	}
	if exists("kb:never-existed") {
		t.Fatal("a context with no file must count as gone")
	}
}
