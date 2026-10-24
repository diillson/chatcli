/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package knowledge

import (
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func linkFixture() *Graph {
	g := New()
	g.AddNode(Node{ID: "project:orbital", Kind: KindProject, Title: "Orbital"})
	g.AddNode(Node{ID: "topic:auth", Kind: KindTopic, Title: "auth"})
	g.AddNode(Node{ID: "tag:auth", Kind: KindTag, Title: "auth"})
	g.AddNode(Node{ID: "fact:1", Kind: KindFact, Title: "f1", Summary: "Orbital uses mTLS, see [[auth]] and [[Missing]]"})
	return g
}

func TestAddLink_FeedsAdjacencyLikeAddEdge(t *testing.T) {
	a, b := linkFixture(), linkFixture()
	a.AddEdge("fact:1", "project:orbital", 2)
	a.AddEdge("fact:1", "project:orbital", 1)
	b.AddLink(Link{From: "fact:1", To: "project:orbital", Rel: RelInProject, Weight: 2})
	b.AddLink(Link{From: "fact:1", To: "project:orbital", Rel: RelMentions, Weight: 1})
	if !reflect.DeepEqual(a.EdgeList(), b.EdgeList()) {
		t.Fatalf("adjacency differs:\nAddEdge %+v\nAddLink %+v", a.EdgeList(), b.EdgeList())
	}
	if b.LinkCount() != 2 {
		t.Fatalf("LinkCount = %d, want 2 (distinct rels)", b.LinkCount())
	}
}

func TestAddLink_RejectsMissingEndpointsAndSelf(t *testing.T) {
	g := linkFixture()
	if g.AddLink(Link{From: "fact:1", To: "nope"}) || g.AddLink(Link{From: "fact:1", To: "fact:1"}) {
		t.Fatal("invalid link accepted")
	}
	if g.Edges() != 0 || g.LinkCount() != 0 {
		t.Fatal("invalid link touched the graph")
	}
}

func TestAddLink_DefaultsAndAccumulation(t *testing.T) {
	g := linkFixture()
	g.AddLink(Link{From: "fact:1", To: "topic:auth"})
	g.AddLink(Link{From: "fact:1", To: "topic:auth", Origin: OriginUser, Weight: 2})
	out := g.Outlinks("fact:1")
	if len(out) != 1 {
		t.Fatalf("outlinks = %+v", out)
	}
	l := out[0]
	if l.Rel != RelRelated || l.Origin != OriginUser || l.Weight != 3 {
		t.Fatalf("link = %+v, want related/user/3", l)
	}
}

func TestOutlinksBacklinksDirection(t *testing.T) {
	g := linkFixture()
	g.AddLink(Link{From: "topic:auth", To: "fact:1", Rel: RelAbout, Weight: 2})
	g.AddLink(Link{From: "fact:1", To: "project:orbital", Rel: RelInProject, Weight: 2})
	if out := g.Outlinks("fact:1"); len(out) != 1 || out[0].To != "project:orbital" {
		t.Fatalf("outlinks = %+v", out)
	}
	if in := g.Backlinks("fact:1"); len(in) != 1 || in[0].From != "topic:auth" {
		t.Fatalf("backlinks = %+v", in)
	}
	if bt := g.LinksBetween("project:orbital", "fact:1"); len(bt) != 1 {
		t.Fatalf("LinksBetween = %+v", bt)
	}
}

func TestResolve_PriorityAliasesAndNormalization(t *testing.T) {
	g := linkFixture()
	// "auth" names both a topic and a tag: the topic wins.
	if n, ok := g.Resolve("auth"); !ok || n.ID != "topic:auth" {
		t.Fatalf("Resolve(auth) = %+v", n)
	}
	if all := g.ResolveAll("AUTH"); len(all) != 2 || all[0].ID != "topic:auth" || all[1].ID != "tag:auth" {
		t.Fatalf("ResolveAll order = %+v", all)
	}
	g.AddNode(Node{ID: "topic:config", Kind: KindTopic, Title: "Configuração do servidor"})
	if n, ok := g.Resolve("configuracao-do_servidor"); !ok || n.ID != "topic:config" {
		t.Fatalf("accent/separator folding failed: %+v", n)
	}
	g.AddAlias("project:orbital", "orb")
	if n, ok := g.Resolve("Orb"); !ok || n.ID != "project:orbital" {
		t.Fatalf("alias resolution failed: %+v", n)
	}
	if n, ok := g.Resolve("fact:1"); !ok || n.ID != "fact:1" {
		t.Fatal("exact id must resolve")
	}
	if _, ok := g.Resolve("nothing here"); ok {
		t.Fatal("unknown reference resolved")
	}
}

func TestResolve_IndexInvalidatedByNewNodes(t *testing.T) {
	g := linkFixture()
	if _, ok := g.Resolve("later"); ok {
		t.Fatal("unexpected")
	}
	g.AddNode(Node{ID: "topic:later", Kind: KindTopic, Title: "Later"})
	if _, ok := g.Resolve("later"); !ok {
		t.Fatal("stale resolution index after AddNode")
	}
}

func TestParseWikilinks(t *testing.T) {
	got := ParseWikilinks("a [[One]] b [[Two|label]] [[Three#Heading]] ![[Four^block]] [[One]] [[]] [[bad\nline]] [[open")
	want := []string{"One", "Two", "Three", "Four"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseWikilinks = %q, want %q", got, want)
	}
}

func TestLinkWikilinks_ResolvesAndSkipsUnknown(t *testing.T) {
	g := linkFixture()
	// [[auth]] → topic:auth (resolution) + tag:auth (the legacy target);
	// [[Missing]] resolves to nothing.
	if n := g.LinkWikilinks(); n != 2 {
		t.Fatalf("LinkWikilinks added %d, want 2", n)
	}
	var topic *Link
	for _, l := range g.Outlinks("fact:1") {
		if l.To == "topic:auth" {
			l := l
			topic = &l
		}
	}
	if topic == nil || topic.Rel != RelLinksTo || topic.Origin != OriginWikilink || topic.Weight != WikilinkWeight {
		t.Fatalf("wikilink = %+v", g.Outlinks("fact:1"))
	}
}

func TestLinkMentions(t *testing.T) {
	g := New()
	g.AddNode(Node{ID: "project:orbital", Kind: KindProject, Title: "Orbital"})
	g.AddNode(Node{ID: "topic:k8s-operator", Kind: KindTopic, Title: "Kubernetes operator"})
	g.AddNode(Node{ID: "topic:k8s", Kind: KindTopic, Title: "Kubernetes"})
	g.AddNode(Node{ID: "topic:go", Kind: KindTopic, Title: "Go"})       // too short
	g.AddNode(Node{ID: "topic:tests", Kind: KindTopic, Title: "tests"}) // stopword
	g.AddNode(Node{ID: "fact:a", Kind: KindFact, Title: "a", Summary: "The Kubernetes operator in orbital is written in Go, with tests."})
	g.AddNode(Node{ID: "fact:b", Kind: KindFact, Title: "b", Summary: "Orbitals are unrelated (no whole-word match)."})
	g.AddNode(Node{ID: "fact:c", Kind: KindFact, Title: "c", Summary: "Orbital again"})
	g.AddLink(Link{From: "fact:c", To: "project:orbital", Rel: RelInProject, Weight: 2})

	added := g.LinkMentions(MentionOptions{
		SourceKinds: []Kind{KindFact},
		TargetKinds: []Kind{KindProject, KindTopic},
	})
	if added != 2 {
		t.Fatalf("mentions added = %d, want 2; links %+v", added, g.Links())
	}
	targets := map[string]bool{}
	for _, l := range g.Outlinks("fact:a") {
		if l.Rel == RelMentions {
			targets[l.To] = true
		}
	}
	if !targets["topic:k8s-operator"] || !targets["project:orbital"] || targets["topic:k8s"] || targets["topic:go"] || targets["topic:tests"] {
		t.Fatalf("fact:a mention targets = %v", targets)
	}
	if len(g.Outlinks("fact:b")) != 0 {
		t.Fatal("partial-word match autolinked")
	}
	for _, l := range g.Outlinks("fact:c") {
		if l.Rel == RelMentions {
			t.Fatal("mention restated an existing link")
		}
	}
}

func TestLinkMentions_CapPerSource(t *testing.T) {
	g := New()
	var prose []string
	for _, name := range []string{"alpha", "bravo", "charlie", "delta", "echo"} {
		g.AddNode(Node{ID: "topic:" + name, Kind: KindTopic, Title: name})
		prose = append(prose, name)
	}
	g.AddNode(Node{ID: "fact:x", Kind: KindFact, Title: "x", Summary: strings.Join(prose, " ")})
	if n := g.LinkMentions(MentionOptions{SourceKinds: []Kind{KindFact}, TargetKinds: []Kind{KindTopic}, MaxPerSource: 3}); n != 3 {
		t.Fatalf("cap ignored: %d", n)
	}
}

func TestSerialize_RoundTripsLinksAndAliases(t *testing.T) {
	g := linkFixture()
	g.AddAlias("project:orbital", "orb")
	g.AddLink(Link{From: "topic:auth", To: "fact:1", Rel: RelAbout, Weight: 2})
	g.LinkWikilinks()
	data, err := g.MarshalGraph()
	if err != nil {
		t.Fatal(err)
	}
	h, err := UnmarshalGraph(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g.Links(), h.Links()) {
		t.Fatalf("links lost:\n%+v\n%+v", g.Links(), h.Links())
	}
	if !reflect.DeepEqual(g.EdgeList(), h.EdgeList()) {
		t.Fatal("adjacency changed on round trip (link replay must not add weight)")
	}
	if n, ok := h.Resolve("orb"); !ok || n.ID != "project:orbital" {
		t.Fatal("alias lost on round trip")
	}
	again, _ := h.MarshalGraph()
	if string(again) != string(data) {
		t.Fatal("serialization not byte-stable")
	}
}

func TestUnmarshal_LegacyPayloadWithoutLinks(t *testing.T) {
	legacy := `{"nodes":[{"id":"a","kind":"fact","title":"A"},{"id":"b","kind":"topic","title":"B"}],"edges":[{"a":"a","b":"b","w":2}]}`
	g, err := UnmarshalGraph([]byte(legacy))
	if err != nil {
		t.Fatal(err)
	}
	if g.Edges() != 1 || g.LinkCount() != 0 {
		t.Fatalf("legacy payload: edges=%d links=%d", g.Edges(), g.LinkCount())
	}
}

func TestFrozenGraph_ConcurrentResolve(t *testing.T) {
	g := linkFixture()
	g.Freeze()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				if _, ok := g.Resolve("auth"); !ok {
					t.Error("resolve failed")
					return
				}
				_ = g.Backlinks("fact:1")
			}
		}()
	}
	wg.Wait()
}

func TestIndexCard_DocsCountedButNotHubs(t *testing.T) {
	g := New()
	g.AddNode(Node{ID: "kb:x", Kind: KindKB, Title: "handbook"})
	for _, d := range []string{"a", "b", "c"} {
		g.AddNode(Node{ID: "doc:x:" + d, Kind: KindDoc, Title: "Doc " + d})
		g.AddLink(Link{From: "doc:x:" + d, To: "kb:x", Rel: RelPartOf})
	}
	g.AddLink(Link{From: "doc:x:a", To: "doc:x:b", Rel: RelLinksTo})
	g.AddLink(Link{From: "doc:x:a", To: "doc:x:c", Rel: RelLinksTo})
	// The context is a hub through its own links (a tag), never through
	// the documents it holds.
	if strings.Contains(g.IndexCard(2), "Hubs:") {
		t.Fatalf("documents lent hub degree: %s", g.IndexCard(2))
	}
	g.AddNode(Node{ID: "tag:handbook", Kind: KindTag, Title: "handbook-tag"})
	g.AddLink(Link{From: "kb:x", To: "tag:handbook", Rel: RelTagged})
	card := g.IndexCard(2)
	if !strings.Contains(card, "doc 3") {
		t.Fatalf("doc tally missing: %s", card)
	}
	if strings.Contains(card, "Doc a") {
		t.Fatalf("document became a hub: %s", card)
	}
	if !strings.Contains(card, "handbook") {
		t.Fatalf("context hub missing: %s", card)
	}
}

func TestSerialize_ResidualEdgesOnly(t *testing.T) {
	g := linkFixture()
	g.AddLink(Link{From: "fact:1", To: "project:orbital", Rel: RelInProject, Weight: 2})
	g.AddLink(Link{From: "project:orbital", To: "fact:1", Rel: RelMentions, Weight: 1})
	g.AddEdge("fact:1", "project:orbital", 0.5) // untyped remainder
	g.AddEdge("topic:auth", "tag:auth", 1)      // untyped edge
	data, err := g.MarshalGraph()
	if err != nil {
		t.Fatal(err)
	}
	var dto graphDTO
	_ = json.Unmarshal(data, &dto)
	if !dto.Residual || len(dto.Edges) != 2 {
		t.Fatalf("residual edges = %+v", dto.Edges)
	}
	h, err := UnmarshalGraph(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g.EdgeList(), h.EdgeList()) || !reflect.DeepEqual(g.Links(), h.Links()) {
		t.Fatalf("round trip changed the graph:\n%+v\n%+v", g.EdgeList(), h.EdgeList())
	}
}

func TestUnmarshal_FullEdgeFormatWithLinks(t *testing.T) {
	// Written by a build that serialized every edge plus metadata links.
	full := `{"nodes":[{"id":"a","kind":"fact"},{"id":"b","kind":"topic"}],"edges":[{"a":"a","b":"b","w":3}],"links":[{"from":"b","to":"a","rel":"about","w":3}]}`
	g, err := UnmarshalGraph([]byte(full))
	if err != nil {
		t.Fatal(err)
	}
	if nb := g.Neighbors("a"); len(nb) != 1 || nb[0].Weight != 3 {
		t.Fatalf("full-edge payload weights doubled or lost: %+v", nb)
	}
	if len(g.Backlinks("a")) != 1 {
		t.Fatal("links lost")
	}
}

func TestLinkWikilinks_KeepsLegacyTargetOnCollision(t *testing.T) {
	g := New()
	g.AddNode(Node{ID: "tag:auth", Kind: KindTag, Title: "auth"})
	g.AddNode(Node{ID: "topic:auth", Kind: KindTopic, Title: "auth"})
	g.AddNode(Node{ID: "fact:1", Kind: KindFact, Title: "f", Summary: "see [[auth]]"})
	g.AddNode(Node{ID: "doc:c:a.md", Kind: KindDoc, Title: "A", Summary: "doc text [[auth]]"})
	g.LinkWikilinks()
	if !hasRel(g.Outlinks("fact:1"), "topic:auth") || !hasRel(g.Outlinks("fact:1"), "tag:auth") {
		t.Fatalf("collision must keep the legacy tag edge and add the topic: %+v", g.Outlinks("fact:1"))
	}
	if len(g.Outlinks("doc:c:a.md")) != 0 {
		t.Fatal("document summaries are linked by the document pass, not here")
	}
}

func TestLinkMentions_LongestNameOwnsItsTokens(t *testing.T) {
	g := New()
	g.AddNode(Node{ID: "topic:ko", Kind: KindTopic, Title: "kubernetes operator"})
	g.AddNode(Node{ID: "project:operator", Kind: KindProject, Title: "operator"})
	g.AddNode(Node{ID: "fact:1", Kind: KindFact, Title: "f", Summary: "the kubernetes operator restarts pods"})
	g.AddLink(Link{From: "fact:1", To: "topic:ko", Rel: RelAbout})
	g.LinkMentions(MentionOptions{SourceKinds: []Kind{KindFact}, TargetKinds: []Kind{KindTopic, KindProject}, MinRunes: 3})
	if hasRel(g.Outlinks("fact:1"), "project:operator") {
		t.Fatal("a name inside an already linked longer name was linked again")
	}
	h := New()
	h.AddNode(Node{ID: "project:zeta", Kind: KindProject, Title: "Zeta"})
	h.AddAlias("project:zeta", "backend")
	h.AddNode(Node{ID: "fact:2", Kind: KindFact, Title: "f", Summary: "the backend restarts pods"})
	if n := h.LinkMentions(MentionOptions{SourceKinds: []Kind{KindFact}, TargetKinds: []Kind{KindProject}, MinRunes: 3}); n != 0 {
		t.Fatal("aliases must not autolink prose")
	}
}

func hasRel(ls []Link, to string) bool {
	for _, l := range ls {
		if l.To == to {
			return true
		}
	}
	return false
}
