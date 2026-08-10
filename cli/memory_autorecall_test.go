/*
 * ChatCLI - Proactive auto-recall tests.
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 */
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/pkg/knowledge"
)

func newAutoRecallCLI(t *testing.T) *ChatCLI {
	t.Helper()
	cli := newTestCLIWithMemory(t)
	mgr := cli.memoryStore.Manager()
	if !mgr.Facts.AddFact("embed.FS requires forward slashes, never filepath.Join", "gotcha", []string{"embed", "windows"}) {
		t.Fatal("seed fact 1")
	}
	if !mgr.Facts.AddFact("The scheduler daemon re-arms intervals in place", "architecture", []string{"scheduler"}) {
		t.Fatal("seed fact 2")
	}
	return cli
}

func TestMemoryAutoRecallBlock_MatchesHints(t *testing.T) {
	cli := newAutoRecallCLI(t)

	block := cli.memoryAutoRecallBlock([]string{"embed", "windows", "paths"})
	if !strings.Contains(block, "[MEMORY AUTO-RECALL]") {
		t.Fatalf("expected auto-recall header, got %q", block)
	}
	if !strings.Contains(block, "forward slashes") {
		t.Errorf("expected the matching gotcha, got %q", block)
	}
	if strings.Contains(block, "scheduler daemon") {
		t.Errorf("unrelated fact must not ride along, got %q", block)
	}
}

func TestMemoryAutoRecallBlock_EmptyCases(t *testing.T) {
	cli := newAutoRecallCLI(t)

	if got := cli.memoryAutoRecallBlock(nil); got != "" {
		t.Errorf("no hints must inject nothing (empty hints would dump ALL facts), got %q", got)
	}
	if got := cli.memoryAutoRecallBlock([]string{"kubernetes", "istio"}); got != "" {
		t.Errorf("no matching fact must inject nothing, got %q", got)
	}

	t.Setenv("CHATCLI_MEMORY_AUTORECALL", "off")
	if got := cli.memoryAutoRecallBlock([]string{"embed"}); got != "" {
		t.Errorf("disabled auto-recall must inject nothing, got %q", got)
	}

	bare := &ChatCLI{}
	if got := bare.memoryAutoRecallBlock([]string{"embed"}); got != "" {
		t.Errorf("no memory store must inject nothing, got %q", got)
	}
}

func TestMemoryAutoRecallBlock_CapsFactCount(t *testing.T) {
	cli := newAutoRecallCLI(t)
	mgr := cli.memoryStore.Manager()
	for _, c := range []string{"alpha", "beta", "gamma", "delta", "epsilon"} {
		mgr.Facts.AddFact("gateway detail "+c+" for routing", "pattern", []string{"gateway"})
	}

	block := cli.memoryAutoRecallBlock([]string{"gateway", "routing"})
	if block == "" {
		t.Fatal("expected a block")
	}
	lines := 0
	for _, l := range strings.Split(block, "\n") {
		if strings.HasPrefix(l, "- ") {
			lines++
		}
	}
	if lines > autoRecallMaxFacts {
		t.Errorf("block must cap at %d facts, got %d:\n%s", autoRecallMaxFacts, lines, block)
	}
}

func TestMemoryAutoRecallBlock_NoGraphNoRelatedLine(t *testing.T) {
	cli := newAutoRecallCLI(t)
	// Cache never wired → KnowledgeGraph() is nil → no Related line, and
	// crucially no derive-per-call fallback for this nudge.
	block := cli.memoryAutoRecallBlock([]string{"embed", "windows"})
	if strings.Contains(block, "Related (graph)") {
		t.Fatalf("Related line rendered without a cached graph:\n%s", block)
	}
}

func TestMemoryAutoRecallBlock_GraphExpansionLine(t *testing.T) {
	cli := newAutoRecallCLI(t)
	mgr := cli.memoryStore.Manager()

	var embedID string
	for _, f := range mgr.Facts.GetAll() {
		if strings.Contains(f.Content, "embed.FS") {
			embedID = f.ID
		}
	}
	g := knowledge.New()
	g.AddNode(knowledge.Node{ID: "fact:" + embedID, Kind: knowledge.KindFact, Title: "embed gotcha"})
	g.AddNode(knowledge.Node{ID: "episode:e1", Kind: knowledge.KindEpisode, Title: "2026-08-01 windows fix"})
	g.AddNode(knowledge.Node{ID: "topic:windows", Kind: knowledge.KindTopic, Title: "windows"})
	g.AddNode(knowledge.Node{ID: "tag:go", Kind: knowledge.KindTag, Title: "go"})
	g.AddNode(knowledge.Node{ID: "profile:user", Kind: knowledge.KindProfile, Title: "user"})
	g.AddEdge("fact:"+embedID, "episode:e1", 2)
	g.AddEdge("fact:"+embedID, "topic:windows", 1.5)
	g.AddEdge("fact:"+embedID, "tag:go", 1)
	g.AddEdge("fact:"+embedID, "profile:user", 1)
	mgr.SetGraphSource(func() *knowledge.Graph { return g }, nil)
	// The first Snapshot kicks off the async graph.json persist; wait for the
	// atomic rename to land so TempDir cleanup never races the write.
	t.Cleanup(func() {
		path := filepath.Join(mgr.MemoryDir(), "graph.json")
		deadline := time.Now().Add(3 * time.Second)
		for {
			if _, err := os.Stat(path); err == nil || time.Now().After(deadline) {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	})

	block := cli.memoryAutoRecallBlock([]string{"embed", "windows"})
	if !strings.Contains(block, "Related (graph): ") {
		t.Fatalf("Related line missing:\n%s", block)
	}
	if !strings.Contains(block, "2026-08-01 windows fix (episode)") ||
		!strings.Contains(block, "windows (topic)") {
		t.Fatalf("neighbors missing from Related line:\n%s", block)
	}
	if strings.Contains(block, "(tag)") || strings.Contains(block, "(profile)") {
		t.Fatalf("tag/profile glue leaked:\n%s", block)
	}
	if !strings.Contains(block, "@memory neighbors") || strings.Contains(block, "@session") {
		t.Fatalf("pointer wording wrong:\n%s", block)
	}
	// The Related line rides within its own allowance; facts stay intact.
	if !strings.Contains(block, "forward slashes") {
		t.Fatalf("graph line displaced a fact:\n%s", block)
	}
}

func TestAutoRecallRelatedLine_FiltersShownFacts(t *testing.T) {
	g := knowledge.New()
	g.AddNode(knowledge.Node{ID: "fact:a", Kind: knowledge.KindFact, Title: "fact a"})
	g.AddNode(knowledge.Node{ID: "fact:b", Kind: knowledge.KindFact, Title: "fact b"})
	g.AddEdge("fact:a", "fact:b", 1)
	// Both facts already shown in the block → nothing new to point at.
	if line := autoRecallRelatedLine(g, []string{"a", "b"}); line != "" {
		t.Fatalf("shown facts must not resurface: %q", line)
	}
	// Only "a" shown → "b" is a legitimate unshown pointer.
	line := autoRecallRelatedLine(g, []string{"a"})
	if !strings.Contains(line, "fact b (fact)") {
		t.Fatalf("unshown fact neighbor missing: %q", line)
	}
}
