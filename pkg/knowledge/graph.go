/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * Package knowledge is the in-core knowledge graph — the "Obsidian in the core"
 * substrate. It is a pure, dependency-free undirected weighted graph over typed
 * nodes (facts, topics, projects, skills, tags, the user). The CLI layer derives
 * one from the existing memory and skill stores on demand; nothing here knows
 * where the data came from, which keeps it trivially testable.
 *
 * Design discipline (the user's token/headroom constraint): the graph is a
 * retrieval index, not prompt payload. Per turn only a tiny IndexCard (a map of
 * content: node counts + hubs) is cheap enough to inject; the actual node
 * neighborhoods are pulled on demand, exactly like memory's index/recall split.
 */
package knowledge

import (
	"sort"
	"strings"
)

// Kind classifies a node. Kinds are stable string constants so IDs and cards
// stay byte-deterministic (and therefore prompt-cache friendly).
type Kind string

const (
	KindFact    Kind = "fact"
	KindTopic   Kind = "topic"
	KindProject Kind = "project"
	KindSkill   Kind = "skill"
	KindProfile Kind = "profile"
	KindTag     Kind = "tag"
)

// Node is one vertex. ID is unique and namespaced by kind (e.g. "topic:auth").
// Weight is an intrinsic relevance prior (a fact's score, a topic's mentions)
// used to break ties in ranking; it is not the edge weight.
type Node struct {
	ID      string
	Kind    Kind
	Title   string
	Summary string
	Weight  float64
}

// Neighbor is an adjacent node and the accumulated weight of the edge to it.
type Neighbor struct {
	ID     string
	Weight float64
}

// Graph is an undirected weighted multigraph stored as an adjacency map.
type Graph struct {
	nodes map[string]*Node
	adj   map[string]map[string]float64
}

// New returns an empty graph.
func New() *Graph {
	return &Graph{
		nodes: make(map[string]*Node),
		adj:   make(map[string]map[string]float64),
	}
}

// AddNode upserts a node by ID. Re-adding an existing ID updates its display
// fields and keeps the larger Weight, never dropping edges. Empty IDs and
// titles are ignored. Returns the stored node.
func (g *Graph) AddNode(n Node) *Node {
	if strings.TrimSpace(n.ID) == "" {
		return nil
	}
	if existing, ok := g.nodes[n.ID]; ok {
		if n.Title != "" {
			existing.Title = n.Title
		}
		if n.Summary != "" {
			existing.Summary = n.Summary
		}
		if n.Weight > existing.Weight {
			existing.Weight = n.Weight
		}
		return existing
	}
	cp := n
	g.nodes[n.ID] = &cp
	return &cp
}

// AddEdge adds (or reinforces) an undirected edge. It is a no-op when either
// endpoint is missing or when a == b, so callers can wire edges optimistically
// without pre-checking existence. Repeated edges accumulate weight.
func (g *Graph) AddEdge(a, b string, w float64) {
	if a == b {
		return
	}
	if _, ok := g.nodes[a]; !ok {
		return
	}
	if _, ok := g.nodes[b]; !ok {
		return
	}
	if w <= 0 {
		w = 1
	}
	if g.adj[a] == nil {
		g.adj[a] = make(map[string]float64)
	}
	if g.adj[b] == nil {
		g.adj[b] = make(map[string]float64)
	}
	g.adj[a][b] += w
	g.adj[b][a] += w
}

// Node returns a node by ID.
func (g *Graph) Node(id string) (*Node, bool) {
	n, ok := g.nodes[id]
	return n, ok
}

// Nodes returns every node sorted by ID, for deterministic iteration (e.g. the
// wikilink-resolution pass).
func (g *Graph) Nodes() []*Node {
	out := make([]*Node, 0, len(g.nodes))
	for _, n := range g.nodes {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Len is the node count.
func (g *Graph) Len() int { return len(g.nodes) }

// Edges is the undirected edge count.
func (g *Graph) Edges() int {
	total := 0
	for _, m := range g.adj {
		total += len(m)
	}
	return total / 2
}

// CountByKind tallies nodes per kind.
func (g *Graph) CountByKind() map[Kind]int {
	out := make(map[Kind]int)
	for _, n := range g.nodes {
		out[n.Kind]++
	}
	return out
}

// Degree is the number of distinct neighbors of id.
func (g *Graph) Degree(id string) int { return len(g.adj[id]) }

// Neighbors returns the adjacent nodes ordered by edge weight (desc), then ID
// (asc) for stability.
func (g *Graph) Neighbors(id string) []Neighbor {
	adj := g.adj[id]
	out := make([]Neighbor, 0, len(adj))
	for nid, w := range adj {
		out = append(out, Neighbor{ID: nid, Weight: w})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Weight != out[j].Weight {
			return out[i].Weight > out[j].Weight
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Hubs returns the most connected nodes (weighted degree, then intrinsic
// weight, then ID), capped at limit. Hubs are the backbone of the index card.
func (g *Graph) Hubs(limit int) []*Node {
	type scored struct {
		n    *Node
		wdeg float64
		deg  int
	}
	ranked := make([]scored, 0, len(g.nodes))
	for id, n := range g.nodes {
		var w float64
		for _, ew := range g.adj[id] {
			w += ew
		}
		ranked = append(ranked, scored{n: n, wdeg: w, deg: len(g.adj[id])})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].deg != ranked[j].deg {
			return ranked[i].deg > ranked[j].deg
		}
		if ranked[i].wdeg != ranked[j].wdeg {
			return ranked[i].wdeg > ranked[j].wdeg
		}
		if ranked[i].n.Weight != ranked[j].n.Weight {
			return ranked[i].n.Weight > ranked[j].n.Weight
		}
		return ranked[i].n.ID < ranked[j].n.ID
	})
	out := make([]*Node, 0, limit)
	for _, s := range ranked {
		if s.deg == 0 {
			continue // isolated nodes are not hubs
		}
		out = append(out, s.n)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// Search ranks nodes by how many of the keywords appear in their title or
// summary, breaking ties by intrinsic weight then ID. Only nodes with at least
// one match are returned, capped at limit.
func (g *Graph) Search(keywords []string, limit int) []*Node {
	kw := make([]string, 0, len(keywords))
	for _, k := range keywords {
		if k = strings.ToLower(strings.TrimSpace(k)); k != "" {
			kw = append(kw, k)
		}
	}
	if len(kw) == 0 {
		return nil
	}
	type scored struct {
		n       *Node
		matches int
	}
	var ranked []scored
	for _, n := range g.nodes {
		hay := strings.ToLower(n.Title + " " + n.Summary)
		m := 0
		for _, k := range kw {
			if strings.Contains(hay, k) {
				m++
			}
		}
		if m > 0 {
			ranked = append(ranked, scored{n: n, matches: m})
		}
	}
	if len(ranked) == 0 {
		return nil
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].matches != ranked[j].matches {
			return ranked[i].matches > ranked[j].matches
		}
		if ranked[i].n.Weight != ranked[j].n.Weight {
			return ranked[i].n.Weight > ranked[j].n.Weight
		}
		return ranked[i].n.ID < ranked[j].n.ID
	})
	out := make([]*Node, 0, limit)
	for _, s := range ranked {
		out = append(out, s.n)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// Neighborhood returns the nodes reachable within `hops` of id (a breadth-first
// local graph), excluding the seed, ordered by hop distance then edge weight,
// capped at limit. This is the on-demand "pull" — the local graph of a node.
func (g *Graph) Neighborhood(id string, hops, limit int) []*Node {
	if _, ok := g.nodes[id]; !ok || hops < 1 {
		return nil
	}
	visited := map[string]bool{id: true}
	type item struct {
		id   string
		dist int
		w    float64
	}
	var order []item
	frontier := []string{id}
	for d := 1; d <= hops && len(frontier) > 0; d++ {
		var next []string
		for _, cur := range frontier {
			for _, nb := range g.Neighbors(cur) {
				if visited[nb.ID] {
					continue
				}
				visited[nb.ID] = true
				order = append(order, item{id: nb.ID, dist: d, w: nb.Weight})
				next = append(next, nb.ID)
			}
		}
		frontier = next
	}
	sort.SliceStable(order, func(i, j int) bool {
		if order[i].dist != order[j].dist {
			return order[i].dist < order[j].dist
		}
		return order[i].w > order[j].w
	})
	out := make([]*Node, 0, limit)
	for _, it := range order {
		out = append(out, g.nodes[it.id])
		if len(out) >= limit {
			break
		}
	}
	return out
}
