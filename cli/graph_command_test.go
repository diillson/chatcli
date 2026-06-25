/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/pkg/knowledge"
)

func dotSampleGraph() *knowledge.Graph {
	g := knowledge.New()
	g.AddNode(knowledge.Node{ID: "topic:auth", Kind: knowledge.KindTopic, Title: "auth"})
	g.AddNode(knowledge.Node{ID: "fact:1", Kind: knowledge.KindFact, Title: "uses OAuth"})
	g.AddNode(knowledge.Node{ID: "skill:deploy", Kind: knowledge.KindSkill, Title: "deploy"})
	g.AddEdge("topic:auth", "fact:1", 2)
	g.AddEdge("topic:auth", "skill:deploy", 1)
	return g
}

func TestGraphToDOT(t *testing.T) {
	g := dotSampleGraph()
	include := selectFullGraphNodes(g)
	dot := graphToDOT(g, include, "knowledge graph")

	for _, want := range []string{
		"graph knowledge {",
		`"topic:auth" [label="auth"`,
		`"fact:1"`,
		" -- ", // an undirected edge
		kindColor(knowledge.KindTopic),
		kindColor(knowledge.KindSkill),
	} {
		if !strings.Contains(dot, want) {
			t.Fatalf("DOT missing %q in:\n%s", want, dot)
		}
	}
	// Edges are emitted once (deduped): exactly two among three nodes here.
	if n := strings.Count(dot, " -- "); n != 2 {
		t.Fatalf("expected 2 undirected edges, got %d:\n%s", n, dot)
	}
}

func TestSelectFullGraphNodesSmallIncludesAll(t *testing.T) {
	g := dotSampleGraph()
	if got := len(selectFullGraphNodes(g)); got != 3 {
		t.Fatalf("small graph should include all 3 nodes, got %d", got)
	}
}

func TestKindColorDistinct(t *testing.T) {
	kinds := []knowledge.Kind{
		knowledge.KindProfile, knowledge.KindProject, knowledge.KindTopic,
		knowledge.KindSkill, knowledge.KindTag, knowledge.KindFact,
	}
	seen := map[string]bool{}
	for _, k := range kinds {
		c := kindColor(k)
		if c == "" || seen[c] {
			t.Fatalf("kind %q has a missing or duplicate color %q", k, c)
		}
		seen[c] = true
	}
}

func TestHandleGraphCommandEmptyGraph(t *testing.T) {
	// No memory store and no persona handler → empty graph → no render, no panic.
	newTestCLI().handleGraphCommand(context.Background(), "/graph")
}

func TestRenderGraphProducesPNG(t *testing.T) {
	if testing.Short() {
		t.Skip("skips the go-graphviz rasterization under -short")
	}
	g := dotSampleGraph()
	dot := graphToDOT(g, selectFullGraphNodes(g), "test")
	out := filepath.Join(t.TempDir(), "graph.png")
	if _, err := plugins.RenderDOTToFile(context.Background(), dot, "png", "", out); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	fi, err := os.Stat(out)
	if err != nil || fi.Size() == 0 {
		t.Fatalf("expected a non-empty PNG at %s: %v", out, err)
	}
}
