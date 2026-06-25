/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package knowledge

import (
	"strings"
	"testing"
)

// sampleGraph builds a small graph: a topic hub linked to three facts, one of
// which belongs to a project; a skill linked to the topic; plus an isolated tag.
func sampleGraph() *Graph {
	g := New()
	g.AddNode(Node{ID: "topic:auth", Kind: KindTopic, Title: "auth", Weight: 5})
	g.AddNode(Node{ID: "fact:1", Kind: KindFact, Title: "uses OAuth", Summary: "the app uses OAuth2 PKCE", Weight: 3})
	g.AddNode(Node{ID: "fact:2", Kind: KindFact, Title: "token TTL", Summary: "tokens live 1h", Weight: 2})
	g.AddNode(Node{ID: "fact:3", Kind: KindFact, Title: "login flow", Summary: "loopback login", Weight: 1})
	g.AddNode(Node{ID: "project:app", Kind: KindProject, Title: "app", Weight: 4})
	g.AddNode(Node{ID: "skill:deploy", Kind: KindSkill, Title: "deploy", Summary: "how to deploy", Weight: 1})
	g.AddNode(Node{ID: "tag:lonely", Kind: KindTag, Title: "lonely"})

	g.AddEdge("topic:auth", "fact:1", 2)
	g.AddEdge("topic:auth", "fact:2", 2)
	g.AddEdge("topic:auth", "fact:3", 1)
	g.AddEdge("fact:1", "project:app", 1)
	g.AddEdge("skill:deploy", "topic:auth", 1)
	return g
}

func TestAddNodeUpsertAndEdgeGuards(t *testing.T) {
	g := New()
	g.AddNode(Node{ID: "a", Kind: KindFact, Title: "first", Weight: 1})
	g.AddNode(Node{ID: "a", Kind: KindFact, Title: "second", Weight: 5}) // upsert
	if n, _ := g.Node("a"); n.Title != "second" || n.Weight != 5 {
		t.Fatalf("upsert failed: %+v", n)
	}
	if g.Len() != 1 {
		t.Fatalf("upsert created a duplicate: len=%d", g.Len())
	}
	// Edge guards: self-loop and missing endpoints are no-ops.
	g.AddEdge("a", "a", 1)
	g.AddEdge("a", "ghost", 1)
	if g.Edges() != 0 {
		t.Fatalf("edge guards failed: edges=%d", g.Edges())
	}
}

func TestNeighborsHubsSearch(t *testing.T) {
	g := sampleGraph()

	// The topic is the hub.
	hubs := g.Hubs(1)
	if len(hubs) != 1 || hubs[0].ID != "topic:auth" {
		t.Fatalf("expected topic:auth as top hub, got %+v", hubs)
	}
	// Isolated tag is never a hub.
	for _, h := range g.Hubs(10) {
		if h.ID == "tag:lonely" {
			t.Fatal("isolated node ranked as a hub")
		}
	}
	// Neighbors are weight-ordered, ties broken by ID asc: fact:1(2), fact:2(2),
	// then the w1 group fact:3, skill:deploy.
	nb := g.Neighbors("topic:auth")
	if len(nb) != 4 || nb[0].Weight != 2 || nb[2].ID != "fact:3" || nb[3].ID != "skill:deploy" {
		t.Fatalf("neighbor ordering wrong: %+v", nb)
	}
	// Search hits summaries.
	hits := g.Search([]string{"oauth"}, 5)
	if len(hits) != 1 || hits[0].ID != "fact:1" {
		t.Fatalf("search miss: %+v", hits)
	}
	if g.Search([]string{"nonexistent"}, 5) != nil {
		t.Fatal("search should return nil on no match")
	}
}

func TestNeighborhoodBFS(t *testing.T) {
	g := sampleGraph()
	// project:app is one hop from fact:1, two hops from topic:auth.
	hood := g.Neighborhood("project:app", 2, 10)
	var ids []string
	for _, n := range hood {
		ids = append(ids, n.ID)
	}
	if len(ids) == 0 || ids[0] != "fact:1" {
		t.Fatalf("nearest neighbor should be fact:1, got %v", ids)
	}
	// topic:auth is reachable at 2 hops.
	found := false
	for _, id := range ids {
		if id == "topic:auth" {
			found = true
		}
	}
	if !found {
		t.Fatalf("topic:auth should be in 2-hop neighborhood: %v", ids)
	}
	// 1 hop excludes the 2-hop topic.
	near := g.Neighborhood("project:app", 1, 10)
	if len(near) != 1 || near[0].ID != "fact:1" {
		t.Fatalf("1-hop neighborhood wrong: %+v", near)
	}
}

func TestIndexCard(t *testing.T) {
	g := sampleGraph()
	card := g.IndexCard(3)
	for _, want := range []string{"7 nodes", "5 links", "topic 1", "fact 3", "Hubs:", "auth"} {
		if !strings.Contains(card, want) {
			t.Fatalf("index card missing %q in:\n%s", want, card)
		}
	}
	// Determinism: same graph → same card.
	if g.IndexCard(3) != card {
		t.Fatal("index card is not deterministic")
	}
	if New().IndexCard(3) != "" {
		t.Fatal("empty graph should yield empty card")
	}
}
