/*
 * ChatCLI - Tests for graph serialization (serialize.go)
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package knowledge

import (
	"bytes"
	"testing"
)

func buildSampleGraph() *Graph {
	g := New()
	g.AddNode(Node{ID: "fact:1", Kind: KindFact, Title: "uses zap", Summary: "logging via zap", Weight: 2.5})
	g.AddNode(Node{ID: "topic:logging", Kind: KindTopic, Title: "logging", Weight: 3})
	g.AddNode(Node{ID: "tag:go", Kind: KindTag, Title: "go"})
	g.AddNode(Node{ID: "episode:abc", Kind: KindEpisode, Title: "2026-08-01 fixed logger"})
	g.AddEdge("fact:1", "topic:logging", 2)
	g.AddEdge("fact:1", "tag:go", 1)
	g.AddEdge("fact:1", "tag:go", 0.5) // accumulates to 1.5
	g.AddEdge("episode:abc", "fact:1", 1)
	return g
}

func TestMarshalUnmarshalRoundTrip(t *testing.T) {
	g := buildSampleGraph()
	data, err := g.MarshalGraph()
	if err != nil {
		t.Fatalf("MarshalGraph: %v", err)
	}
	got, err := UnmarshalGraph(data)
	if err != nil {
		t.Fatalf("UnmarshalGraph: %v", err)
	}
	if got.Len() != g.Len() || got.Edges() != g.Edges() {
		t.Fatalf("shape mismatch: %d/%d nodes, %d/%d edges",
			got.Len(), g.Len(), got.Edges(), g.Edges())
	}
	// Accumulated edge weight survives exactly.
	for _, nb := range got.Neighbors("fact:1") {
		if nb.ID == "tag:go" && nb.Weight != 1.5 {
			t.Fatalf("accumulated weight = %v, want 1.5", nb.Weight)
		}
	}
	// Node fields survive.
	n, ok := got.Node("fact:1")
	if !ok || n.Kind != KindFact || n.Summary != "logging via zap" || n.Weight != 2.5 {
		t.Fatalf("node round-trip lost fields: %+v", n)
	}
}

func TestMarshalGraphDeterministic(t *testing.T) {
	a, err := buildSampleGraph().MarshalGraph()
	if err != nil {
		t.Fatal(err)
	}
	b, err := buildSampleGraph().MarshalGraph()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("two builds of the same graph marshaled differently")
	}
}

func TestMarshalEmptyGraphRoundTrip(t *testing.T) {
	data, err := New().MarshalGraph()
	if err != nil {
		t.Fatal(err)
	}
	g, err := UnmarshalGraph(data)
	if err != nil {
		t.Fatal(err)
	}
	if g.Len() != 0 || g.Edges() != 0 {
		t.Fatalf("empty round-trip produced %d nodes / %d edges", g.Len(), g.Edges())
	}
}

func TestUnmarshalGraphCorruptInput(t *testing.T) {
	if _, err := UnmarshalGraph([]byte("{not json")); err == nil {
		t.Fatal("corrupt input must error")
	}
}

func TestUnmarshalUnknownKindPreserved(t *testing.T) {
	g := New()
	g.AddNode(Node{ID: "widget:x", Kind: Kind("widget"), Title: "future kind"})
	data, err := g.MarshalGraph()
	if err != nil {
		t.Fatal(err)
	}
	got, err := UnmarshalGraph(data)
	if err != nil {
		t.Fatal(err)
	}
	if n, ok := got.Node("widget:x"); !ok || n.Kind != Kind("widget") {
		t.Fatalf("unknown kind lost in round-trip: %+v", n)
	}
}

func TestEdgeListDedupAndOrder(t *testing.T) {
	g := buildSampleGraph()
	edges := g.EdgeList()
	if len(edges) != g.Edges() {
		t.Fatalf("EdgeList len = %d, want %d", len(edges), g.Edges())
	}
	seen := make(map[string]bool)
	for i, e := range edges {
		if e.A >= e.B {
			t.Fatalf("edge %d not normalized: %q >= %q", i, e.A, e.B)
		}
		key := e.A + "|" + e.B
		if seen[key] {
			t.Fatalf("duplicate edge %s", key)
		}
		seen[key] = true
		if i > 0 {
			prev := edges[i-1]
			if prev.A > e.A || (prev.A == e.A && prev.B > e.B) {
				t.Fatalf("edges not sorted at %d: %+v after %+v", i, e, prev)
			}
		}
	}
}

func TestIndexCardIncludesNewKinds(t *testing.T) {
	g := New()
	g.AddNode(Node{ID: "episode:1", Kind: KindEpisode, Title: "e"})
	g.AddNode(Node{ID: "session:s", Kind: KindSession, Title: "s"})
	g.AddNode(Node{ID: "kb:k", Kind: KindKB, Title: "k"})
	card := g.IndexCard(3)
	for _, want := range []string{"episode 1", "session 1", "kbcontext 1"} {
		if !contains(card, want) {
			t.Fatalf("card missing %q:\n%s", want, card)
		}
	}
}

func contains(s, sub string) bool { return bytes.Contains([]byte(s), []byte(sub)) }
