/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"strings"
	"testing"
)

func TestBuildKnowledgeGraph(t *testing.T) {
	cli := newTestCLIWithMemory(t)
	m := cli.memoryStore.Manager()

	// A project, a fact that belongs to it and wikilinks it, and a topic that
	// links the fact — all relationships the stores already record.
	m.Projects.Upsert(map[string]string{"name": "chatcli", "path": "/repo/chatcli", "description": "the cli"})
	m.Facts.AddFactWithSource("uses OAuth2 PKCE for [[chatcli]]", "architecture", []string{"auth"}, "/repo/chatcli")
	facts := m.Facts.GetAll()
	if len(facts) == 0 {
		t.Fatal("fact was not stored")
	}
	fid := facts[0].ID
	m.Topics.Record([]string{"authentication"})
	m.Topics.LinkFact("authentication", fid)

	g := cli.buildKnowledgeGraph()

	for _, id := range []string{"project:chatcli", "topic:authentication", "fact:" + fid, "tag:auth"} {
		if _, ok := g.Node(id); !ok {
			t.Fatalf("expected node %q in graph", id)
		}
	}

	// topic → fact edge (from LinkFact).
	if nb := g.Neighbors("topic:authentication"); len(nb) == 0 || nb[0].ID != "fact:"+fid {
		t.Fatalf("topic→fact edge missing: %+v", nb)
	}

	// fact → project edge (source project AND the [[chatcli]] wikilink).
	foundProj := false
	for _, nb := range g.Neighbors("fact:" + fid) {
		if nb.ID == "project:chatcli" {
			foundProj = true
		}
	}
	if !foundProj {
		t.Fatal("fact→project edge missing")
	}

	// @memory map / neighbors formatting (graph folded into @memory).
	if idx, _ := cli.graphMapText(); !strings.Contains(idx, "Knowledge graph:") {
		t.Fatalf("map card malformed: %q", idx)
	}
	out, _ := cli.graphNeighborsText("authentication") // free-text resolves to topic:authentication
	if !strings.Contains(out, "Local graph") || !strings.Contains(out, "OAuth2") {
		t.Fatalf("neighbors output malformed:\n%s", out)
	}
}

func TestGraphHelpers(t *testing.T) {
	if graphSlug("  Auth Flow ") != "auth flow" {
		t.Errorf("graphSlug: %q", graphSlug("  Auth Flow "))
	}
	if tagID("Go") != "tag:go" {
		t.Errorf("tagID: %q", tagID("Go"))
	}
	if got := graphTitle("first line\nsecond line"); got != "first line" {
		t.Errorf("graphTitle: %q", got)
	}
}
