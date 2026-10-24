/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * card.go — the graph's Map Of Content (Obsidian MOC). A compact, deterministic
 * digest of the whole graph: how many nodes of each kind, and the hubs. This is
 * the ONLY graph artifact small and stable enough to inject per turn; detail is
 * pulled on demand via Neighborhood/Search.
 */
package knowledge

import (
	"sort"
	"strconv"
	"strings"
)

// kindOrder fixes the display order so the card is byte-stable across turns.
var kindOrder = []Kind{
	KindProfile, KindProject, KindTopic, KindSkill,
	KindEpisode, KindNote, KindSession, KindKB, KindContext, KindDoc, KindCard,
	KindFact, KindTag,
}

// IndexCard renders the map of content: a one-line tally by kind plus up to
// maxHubs hub titles. Returns "" for an empty graph. The output is deterministic
// for a given graph, so it does not bust the prompt cache when unchanged.
func (g *Graph) IndexCard(maxHubs int) string {
	if g.Len() == 0 {
		return ""
	}
	counts := g.CountByKind()

	var tally []string
	for _, k := range kindOrder {
		if c := counts[k]; c > 0 {
			tally = append(tally, string(k)+" "+strconv.Itoa(c))
		}
	}
	// Any kind not in kindOrder (future kinds) appended in sorted order.
	var extra []string
	for k, c := range counts {
		if !knownKind(k) && c > 0 {
			extra = append(extra, string(k)+" "+strconv.Itoa(c))
		}
	}
	sort.Strings(extra)
	tally = append(tally, extra...)

	var b strings.Builder
	b.WriteString("Knowledge graph: ")
	b.WriteString(strconv.Itoa(g.Len()))
	b.WriteString(" nodes, ")
	b.WriteString(strconv.Itoa(g.Edges()))
	b.WriteString(" links (")
	b.WriteString(strings.Join(tally, ", "))
	b.WriteString(").")

	if hubs := g.cardHubs(maxHubs); len(hubs) > 0 {
		titles := make([]string, 0, len(hubs))
		for _, h := range hubs {
			t := strings.TrimSpace(h.Title)
			if t == "" {
				t = h.ID
			}
			titles = append(titles, t)
		}
		b.WriteString(" Hubs: ")
		b.WriteString(strings.Join(titles, ", "))
		b.WriteString(".")
	}
	return b.String()
}

// cardHubs ranks hubs for the per-turn card by their links to the user's
// own notes only: documents and journal notes neither become hubs nor
// lend degree to others. A knowledge base with 2,000 documents, or a busy
// day linking 20 facts, would otherwise crowd projects and topics out of
// the card every turn; they stay reachable through neighbors and the
// tally. Same ordering rules as Hubs otherwise.
func (g *Graph) cardHubs(limit int) []*Node {
	skip := func(k Kind) bool { return k == KindDoc || k == KindNote }
	type scored struct {
		n    *Node
		wdeg float64
		deg  int
	}
	ranked := make([]scored, 0, len(g.nodes))
	for id, n := range g.nodes {
		if skip(n.Kind) {
			continue
		}
		var w float64
		deg := 0
		for nb, ew := range g.adj[id] {
			if other, ok := g.nodes[nb]; ok && skip(other.Kind) {
				continue
			}
			w += ew
			deg++
		}
		ranked = append(ranked, scored{n: n, wdeg: w, deg: deg})
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
			continue
		}
		out = append(out, s.n)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func knownKind(k Kind) bool {
	for _, kk := range kindOrder {
		if kk == k {
			return true
		}
	}
	return false
}
