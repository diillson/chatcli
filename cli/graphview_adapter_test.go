/*
 * ChatCLI - Tests for the @graphview source provider adapter.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"testing"

	"github.com/diillson/chatcli/models"
)

func TestConversationGraphFromHistory(t *testing.T) {
	c := &ChatCLI{history: []models.Message{
		{Role: "system", Content: "ignored"},
		{Role: "user", Content: "como instalo?"},
		{Role: "assistant", Content: "assim", ToolCalls: []models.ToolCall{{Name: "@read"}, {Name: "@read"}}},
		{Role: "tool", Content: "result"},
		{Role: "assistant", Content: "pronto"},
	}}
	a := &graphViewPluginAdapter{cli: c}
	data, err := a.ConversationGraph()
	if err != nil {
		t.Fatalf("conversation graph: %v", err)
	}

	kinds := map[string]int{}
	var hasRoot bool
	for _, n := range data.Nodes {
		kinds[n.Kind]++
		if n.ID == "session:root" {
			hasRoot = true
		}
	}
	if !hasRoot {
		t.Fatal("missing session root node")
	}
	// system + tool messages excluded → 1 user + 2 assistant turns.
	if kinds["user"] != 1 || kinds["assistant"] != 2 {
		t.Fatalf("turn kinds = %+v", kinds)
	}
	// @read called twice but deduped to one tool node.
	if kinds["tool"] != 1 {
		t.Fatalf("tool nodes = %d, want 1", kinds["tool"])
	}
	if kinds["session"] != 1 {
		t.Fatalf("session nodes = %d, want 1", kinds["session"])
	}
	if len(data.Edges) == 0 {
		t.Fatal("expected edges linking the thread")
	}
}

func TestConversationGraphEmptyWhenNoHistory(t *testing.T) {
	a := &graphViewPluginAdapter{cli: &ChatCLI{}}
	data, err := a.ConversationGraph()
	if err != nil {
		t.Fatalf("conversation graph: %v", err)
	}
	// Only the root would exist → reported as empty so the caller shows the
	// friendly message instead of a lone dot.
	if len(data.Nodes) != 0 {
		t.Fatalf("expected empty graph, got %d nodes", len(data.Nodes))
	}
}

func TestKnowledgeGraphEmptyWithoutStores(t *testing.T) {
	a := &graphViewPluginAdapter{cli: &ChatCLI{}}
	data, err := a.KnowledgeGraph()
	if err != nil {
		t.Fatalf("knowledge graph: %v", err)
	}
	if len(data.Nodes) != 0 {
		t.Fatalf("expected empty knowledge graph, got %d nodes", len(data.Nodes))
	}
}
