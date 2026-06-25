/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * graph_command.go — the /graph visualization: renders the in-core knowledge
 * graph to an image (the Obsidian "graph view"). It reuses the embedded
 * go-graphviz engine behind @diagram, so no new rendering dependency is added.
 *
 *   /graph             the whole graph (capped to the top hubs when large)
 *   /graph <subject>   the local graph around the node matching <subject>
 */
package cli

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/pkg/knowledge"
)

const (
	graphVizMaxNodes  = 60 // full-graph node cap; beyond this only the top hubs show
	graphVizHoodHops  = 2
	graphVizHoodLimit = 28
)

// handleGraphCommand renders the knowledge graph (or a subject's local graph).
func (cli *ChatCLI) handleGraphCommand(ctx context.Context, input string) {
	arg := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(input), "/graph"))

	g := cli.buildKnowledgeGraph()
	if g.Len() == 0 {
		fmt.Println(colorize("  "+i18n.T("graph.cmd.empty"), ColorYellow))
		return
	}

	var include map[string]bool
	var title string
	switch {
	case arg == "" || arg == "full" || arg == "all":
		include = selectFullGraphNodes(g)
		title = "knowledge graph"
	default:
		seed, ok := g.Node(arg)
		if !ok {
			if hits := g.Search(strings.Fields(arg), 1); len(hits) > 0 {
				seed = hits[0]
			}
		}
		if seed == nil {
			fmt.Println(colorize("  "+i18n.T("graph.cmd.no_node", arg), ColorYellow))
			return
		}
		include = map[string]bool{seed.ID: true}
		for _, n := range g.Neighborhood(seed.ID, graphVizHoodHops, graphVizHoodLimit) {
			include[n.ID] = true
		}
		title = "graph: " + seed.Title
	}

	dot := graphToDOT(g, include, title)
	summary, err := plugins.RenderDOTToFile(ctx, dot, "png", "", "")
	if err != nil {
		fmt.Println(colorize("  "+i18n.T("graph.cmd.error", err.Error()), ColorRed))
		return
	}
	fmt.Println(colorize("  "+i18n.T("graph.cmd.rendered"), ColorGreen))
	fmt.Println(colorize("  "+summary, ColorGray))
}

// selectFullGraphNodes returns every node when the graph is small, otherwise the
// top hubs, so a large graph renders a readable backbone instead of a hairball.
func selectFullGraphNodes(g *knowledge.Graph) map[string]bool {
	include := make(map[string]bool)
	if g.Len() <= graphVizMaxNodes {
		for _, n := range g.Nodes() {
			include[n.ID] = true
		}
		return include
	}
	for _, h := range g.Hubs(graphVizMaxNodes) {
		include[h.ID] = true
	}
	return include
}

// graphToDOT renders the included nodes and the edges among them as undirected
// DOT, colored by node kind.
func graphToDOT(g *knowledge.Graph, include map[string]bool, title string) string {
	var b strings.Builder
	b.WriteString("graph knowledge {\n")
	fmt.Fprintf(&b, "  label=%q; labelloc=t; fontname=\"sans-serif\"; fontcolor=\"#cccccc\";\n", title)
	b.WriteString("  rankdir=LR; bgcolor=\"transparent\"; overlap=false; splines=true;\n")
	b.WriteString("  node [style=filled, shape=box, fontname=\"sans-serif\", fontsize=10, fontcolor=\"#ffffff\", color=\"#00000000\"];\n")
	b.WriteString("  edge [color=\"#777777\"];\n")

	for _, n := range g.Nodes() {
		if !include[n.ID] {
			continue
		}
		label := strings.TrimSpace(n.Title)
		if label == "" {
			label = n.ID
		}
		fmt.Fprintf(&b, "  %q [label=%q, fillcolor=%q];\n", n.ID, truncateForLog(label, 40), kindColor(n.Kind))
	}

	seen := make(map[string]bool)
	for _, n := range g.Nodes() {
		if !include[n.ID] {
			continue
		}
		for _, nb := range g.Neighbors(n.ID) {
			if !include[nb.ID] {
				continue
			}
			a, c := n.ID, nb.ID
			if a > c {
				a, c = c, a
			}
			key := a + "|" + c
			if seen[key] {
				continue
			}
			seen[key] = true
			pw := 1.0 + math.Min(nb.Weight, 4.0)
			fmt.Fprintf(&b, "  %q -- %q [penwidth=%.1f];\n", a, c, pw)
		}
	}
	b.WriteString("}\n")
	return b.String()
}

// kindColor maps a node kind to a fill color for the graph view.
func kindColor(k knowledge.Kind) string {
	switch k {
	case knowledge.KindProfile:
		return "#d4a017" // gold — the user
	case knowledge.KindProject:
		return "#3b6ea5" // blue
	case knowledge.KindTopic:
		return "#2e8b57" // green
	case knowledge.KindSkill:
		return "#7d5ba6" // purple
	case knowledge.KindTag:
		return "#999999" // light gray
	default:
		return "#555555" // fact — dark gray
	}
}
