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
var kindOrder = []Kind{KindProfile, KindProject, KindTopic, KindSkill, KindFact, KindTag}

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

	if hubs := g.Hubs(maxHubs); len(hubs) > 0 {
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

func knownKind(k Kind) bool {
	for _, kk := range kindOrder {
		if kk == k {
			return true
		}
	}
	return false
}
