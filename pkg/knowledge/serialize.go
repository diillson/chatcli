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
}

// MarshalGraph serializes the graph to compact, deterministic JSON: nodes
// sorted by ID, edges by (A, B).
func (g *Graph) MarshalGraph() ([]byte, error) {
	nodes := g.Nodes() // sorted by ID
	dto := graphDTO{
		Nodes: make([]nodeDTO, 0, len(nodes)),
		Edges: g.EdgeList(),
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
	for _, e := range dto.Edges {
		g.AddEdge(e.A, e.B, e.Weight)
	}
	return g, nil
}
