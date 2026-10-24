/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * serialize.go — deterministic (de)serialization of the graph, plus the
 * exported edge-list accessor. The Graph's internal maps stay unexported and
 * Node grows no json tags (the wire shape is decoupled via DTOs, so the
 * public API surface is untouched); MarshalGraph output is byte-stable for a
 * given graph, which lets a persisted copy double as a change fingerprint.
 * Typed links and aliases ride along, so a persisted graph keeps its
 * backlinks and wikilink resolution.
 */
package knowledge

import (
	"encoding/json"
	"sort"
)

// Edge is one undirected edge with its accumulated weight. Exposed so
// consumers that need the full edge set (serialization, DOT rendering,
// graph-view adapters) share one canonical walk instead of reimplementing
// pair deduplication over Neighbors.
type Edge struct {
	A      string  `json:"a"`
	B      string  `json:"b"`
	Weight float64 `json:"w"`
}

// EdgeList returns every undirected edge exactly once (A < B), sorted by
// (A, B) for deterministic output.
func (g *Graph) EdgeList() []Edge {
	out := make([]Edge, 0, g.Edges())
	for a, nbrs := range g.adj {
		for b, w := range nbrs {
			if a < b {
				out = append(out, Edge{A: a, B: b, Weight: w})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].A != out[j].A {
			return out[i].A < out[j].A
		}
		return out[i].B < out[j].B
	})
	return out
}

// nodeDTO mirrors Node for the wire. Kept unexported and separate so the
// public Node type never grows serialization concerns (apidiff-neutral).
type nodeDTO struct {
	ID      string  `json:"id"`
	Kind    Kind    `json:"kind"`
	Title   string  `json:"title,omitempty"`
	Summary string  `json:"summary,omitempty"`
	Weight  float64 `json:"weight,omitempty"`
}

type graphDTO struct {
	Nodes []nodeDTO `json:"nodes"`
	Edges []Edge    `json:"edges"`
	// Links and Aliases carry the typed-link layer (links.go). Absent in
	// files written before it existed; such a graph simply has no typed
	// links until the next rebuild.
	Links   []Link              `json:"links,omitempty"`
	Aliases map[string][]string `json:"aliases,omitempty"`
	// Residual marks the links-carry-weight format: Edges then hold only
	// adjacency no link explains.
	Residual bool `json:"residual,omitempty"`
}

// LinksOnly reports whether the payload uses the residual-edge format.
func (d graphDTO) LinksOnly() bool { return d.Residual }

// residualEdges returns, per undirected pair, the adjacency weight not
// accounted for by typed links (tolerating float rounding), sorted.
func (g *Graph) residualEdges() []Edge {
	explained := make(map[[2]string]float64, len(g.linkTab))
	for k, l := range g.linkTab {
		a, b := k.from, k.to
		if b < a {
			a, b = b, a
		}
		explained[[2]string{a, b}] += l.Weight
	}
	var out []Edge
	for _, e := range g.EdgeList() {
		// Links never carry more than their edge (AddLink feeds both), so
		// only a positive remainder is meaningful.
		if r := e.Weight - explained[[2]string{e.A, e.B}]; r > 1e-9 {
			out = append(out, Edge{A: e.A, B: e.B, Weight: r})
		}
	}
	if out == nil {
		out = []Edge{}
	}
	return out
}

// MarshalGraph serializes the graph to compact, deterministic JSON: nodes
// sorted by ID, typed links by (From, To, Rel), and only the RESIDUAL edges
// — adjacency weight no typed link accounts for (graphs built with bare
// AddEdge). Every link already carries its share of the edge weight, so
// writing the full edge list too would store the graph twice.
func (g *Graph) MarshalGraph() ([]byte, error) {
	nodes := g.Nodes() // sorted by ID
	links := g.Links()
	dto := graphDTO{
		Nodes:    make([]nodeDTO, 0, len(nodes)),
		Edges:    g.residualEdges(),
		Links:    links,
		Residual: true,
	}
	if len(g.aliases) > 0 {
		dto.Aliases = make(map[string][]string, len(g.aliases))
		for id := range g.aliases {
			dto.Aliases[id] = g.Aliases(id) // sorted: byte-stable output
		}
	}
	for _, n := range nodes {
		dto.Nodes = append(dto.Nodes, nodeDTO{
			ID: n.ID, Kind: n.Kind, Title: n.Title, Summary: n.Summary, Weight: n.Weight,
		})
	}
	return json.Marshal(dto)
}

// UnmarshalGraph rebuilds a graph from MarshalGraph output. Edges are
// replayed after every node exists (AddEdge drops edges with missing
// endpoints), and each serialized edge carries the already-accumulated
// weight, so a single AddEdge per pair reproduces the original exactly.
func UnmarshalGraph(data []byte) (*Graph, error) {
	var dto graphDTO
	if err := json.Unmarshal(data, &dto); err != nil {
		return nil, err
	}
	g := New()
	for _, n := range dto.Nodes {
		g.AddNode(Node(n))
	}
	if dto.LinksOnly() {
		// Current format: links carry their weight; edges are residual.
		for _, l := range dto.Links {
			g.AddLink(l)
		}
		for _, e := range dto.Edges {
			g.AddEdge(e.A, e.B, e.Weight)
		}
	} else {
		// Full-edge format (written before links carried the adjacency):
		// edges hold the accumulated weights; links replay as metadata.
		for _, e := range dto.Edges {
			g.AddEdge(e.A, e.B, e.Weight)
		}
		for _, l := range dto.Links {
			l = normalizeLink(l)
			if _, ok := g.nodes[l.From]; !ok {
				continue
			}
			if _, ok := g.nodes[l.To]; !ok {
				continue
			}
			g.recordLink(l)
		}
	}
	for id, as := range dto.Aliases {
		for _, a := range as {
			g.AddAlias(id, a)
		}
	}
	g.Freeze()
	return g, nil
}
