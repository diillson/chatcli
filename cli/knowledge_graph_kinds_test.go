/*
 * ChatCLI - Tests for the episode/session/kb graph builders (knowledge_graph.go)
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/workspace/memory"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/knowledge"
)

func TestAddEpisodeNodes_ProjectAndSameDayFactEdges(t *testing.T) {
	cli := newTestCLIWithMemory(t)
	m := cli.memoryStore.Manager()

	m.Projects.Upsert(map[string]string{"name": "chatcli", "project_path": "/repo/chatcli"})
	// Fact learned today in the project → same-day bridge to the episode.
	m.Facts.AddFactWithSource("bedrock caps payload at fifty megabytes", "gotcha", nil, "/repo/chatcli")
	facts := m.Facts.GetAll()
	if len(facts) != 1 {
		t.Fatalf("facts = %d, want 1", len(facts))
	}
	m.Episodes.Add(memory.Episode{
		Summary: "shipped the payload cap fallback",
		Outcome: "merged",
		Project: "/repo/chatcli",
		Date:    time.Now(),
	})
	eps := m.Episodes.Range(time.Time{}, time.Time{}, "", "", 0)
	if len(eps) != 1 {
		t.Fatalf("episodes = %d, want 1", len(eps))
	}
	epNode := "episode:" + eps[0].ID

	g := cli.buildKnowledgeGraph()
	n, ok := g.Node(epNode)
	if !ok {
		t.Fatal("episode node missing")
	}
	if n.Kind != knowledge.KindEpisode || !strings.Contains(n.Title, "shipped the payload") {
		t.Fatalf("episode node malformed: %+v", n)
	}
	if !strings.Contains(n.Summary, "→ merged") {
		t.Fatalf("outcome missing from summary: %q", n.Summary)
	}

	var gotProject, gotFact bool
	for _, nb := range g.Neighbors(epNode) {
		switch nb.ID {
		case "project:chatcli":
			gotProject = true
			if nb.Weight != 2 {
				t.Fatalf("episode↔project weight = %v, want 2", nb.Weight)
			}
		case "fact:" + facts[0].ID:
			gotFact = true
			if nb.Weight != 1 {
				t.Fatalf("episode↔fact weight = %v, want 1", nb.Weight)
			}
		}
	}
	if !gotProject {
		t.Fatal("episode↔project edge missing (path keyspace join)")
	}
	if !gotFact {
		t.Fatal("episode↔fact same-day edge missing")
	}
}

func TestAddSessionNodes_CapAndProjectEdge(t *testing.T) {
	cli := newTestCLIWithMemory(t)
	m := cli.memoryStore.Manager()
	m.Projects.Upsert(map[string]string{"name": "chatcli", "project_path": "/repo/chatcli"})

	sm := newTestSessionManager(t)
	cli.sessionManager = sm
	sd := func() *SessionData {
		return &SessionData{Version: 2, ChatHistory: []models.Message{
			{Role: "user", Content: "hello"}, {Role: "assistant", Content: "hi"},
		}}
	}
	// One session whose name carries the project slug, plus filler beyond the cap.
	if err := sm.SaveSessionV2("chatcli-auth-work", sd()); err != nil {
		t.Fatalf("save: %v", err)
	}
	for i := 0; i < graphMaxSessionNodes+5; i++ {
		name := fmt.Sprintf("filler-%03d", i)
		if err := sm.SaveSessionV2(name, sd()); err != nil {
			t.Fatalf("save filler: %v", err)
		}
	}

	g := cli.buildKnowledgeGraph()
	sessions := 0
	for _, n := range g.Nodes() {
		if n.Kind == knowledge.KindSession {
			sessions++
		}
	}
	if sessions == 0 || sessions > graphMaxSessionNodes {
		t.Fatalf("session nodes = %d, want (0, %d]", sessions, graphMaxSessionNodes)
	}
	// The project-slug session must link to the project (1.5).
	if n, ok := g.Node("session:chatcli-auth-work"); ok {
		found := false
		for _, nb := range g.Neighbors(n.ID) {
			if nb.ID == "project:chatcli" && nb.Weight == 1.5 {
				found = true
			}
		}
		if !found {
			t.Fatal("session↔project slug edge missing")
		}
	}
}

func TestKnowledgeGraphAccessor_FallsBackWhenDisabled(t *testing.T) {
	t.Setenv("CHATCLI_MEMORY_GRAPH", "off")
	cli := newTestCLIWithMemory(t)
	cli.memoryStore.Manager().Facts.AddFact("derive per call still works", "general", nil)
	g := cli.knowledgeGraph()
	if g == nil || g.Len() == 0 {
		t.Fatal("disabled accessor must fall back to derive-per-call")
	}
}

func TestKnowledgeGraphAccessor_ServesCachedSnapshotWhenWired(t *testing.T) {
	t.Setenv("CHATCLI_MEMORY_GRAPH", "")
	cli := newTestCLIWithMemory(t)
	cli.memoryStore.Manager().Facts.AddFact("cached snapshot fact", "general", nil)
	cli.wireMemoryGraph()

	g1 := cli.knowledgeGraph()
	g2 := cli.knowledgeGraph()
	if g1 == nil || g1 != g2 {
		t.Fatal("expected the same cached snapshot across clean reads")
	}
	// A mutation marks dirty → next access is a rebuilt (different) snapshot.
	cli.memoryStore.Manager().Facts.AddFact("second fact invalidates", "general", nil)
	if g3 := cli.knowledgeGraph(); g3 == g1 {
		t.Fatal("mutation did not invalidate the cached snapshot")
	}
}
