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
	graphVizMaxNodes  = 40 // full-graph node cap; beyond this only the top hubs show
	graphVizHoodHops  = 2
	graphVizHoodLimit = 24
	graphVizLabelWrap = 18 // characters per label line before wrapping
	graphVizDPI       = 96
	graphVizEngine    = "sfdp" // force-directed, Obsidian-like spatial layout
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
			if n.Kind == knowledge.KindTag {
				continue
			}
			include[n.ID] = true
		}
		title = "graph: " + seed.Title
	}

	dot := graphToDOT(g, include, title)
	summary, err := plugins.RenderDOTToFile(ctx, dot, "png", graphVizEngine, "", graphVizDPI)
	if err != nil {
		fmt.Println(colorize("  "+i18n.T("graph.cmd.error", err.Error()), ColorRed))
		return
	}
	fmt.Println(colorize("  "+i18n.T("graph.cmd.rendered"), ColorGreen))
	fmt.Println(colorize("  "+summary, ColorGray))
}

// selectFullGraphNodes returns the structural nodes to draw: tags are excluded
// (they are keyword glue that turns the view into a hairball). When the graph is
// small all remaining nodes show; otherwise only the top hubs, so a big graph
// renders a readable backbone.
func selectFullGraphNodes(g *knowledge.Graph) map[string]bool {
	include := make(map[string]bool)
	if g.Len() <= graphVizMaxNodes {
		for _, n := range g.Nodes() {
			if n.Kind == knowledge.KindTag {
				continue
			}
			include[n.ID] = true
		}
		return include
	}
	// Pull extra hubs since some will be tags we skip.
	for _, h := range g.Hubs(graphVizMaxNodes * 2) {
		if h.Kind == knowledge.KindTag {
			continue
		}
		include[h.ID] = true
		if len(include) >= graphVizMaxNodes {
			break
		}
	}
	return include
}

// graphToDOT renders the included nodes and the edges among them as undirected
// DOT for a force-directed layout: a solid dark canvas with light, wrapped
// labels and kind colors, so the result is actually legible.
func graphToDOT(g *knowledge.Graph, include map[string]bool, title string) string {
	var b strings.Builder
	b.WriteString("graph knowledge {\n")
	fmt.Fprintf(&b, "  label=%q; labelloc=t; fontsize=18; fontname=\"sans-serif\"; fontcolor=\"#e6e6e6\";\n", title)
	b.WriteString("  bgcolor=\"#1e1e2e\"; overlap=prism; overlap_scaling=2; sep=\"+14\"; splines=true; K=0.9;\n")
	b.WriteString("  node [style=\"rounded,filled\", shape=box, penwidth=0, fontname=\"sans-serif\", fontsize=11, fontcolor=\"#ffffff\", margin=\"0.14,0.07\"];\n")
	b.WriteString("  edge [color=\"#56566e\", penwidth=1.0];\n")

	for _, n := range g.Nodes() {
		if !include[n.ID] {
			continue
		}
		label := strings.TrimSpace(n.Title)
		if label == "" {
			label = n.ID
		}
		fmt.Fprintf(&b, "  %q [label=%q, fillcolor=%q];\n", n.ID, wrapLabel(truncateForLog(label, 48), graphVizLabelWrap), kindColor(n.Kind))
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

// wrapLabel breaks a label on word boundaries to at most width characters per
// line and at most three lines (the rest elided), so long fact titles do not
// blow up a node. Newlines use the DOT label convention.
func wrapLabel(s string, width int) string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return s
	}
	var lines []string
	cur := ""
	for _, w := range words {
		switch {
		case cur == "":
			cur = w
		case len(cur)+1+len(w) > width:
			lines = append(lines, cur)
			cur = w
		default:
			cur += " " + w
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	if len(lines) > 3 {
		lines = lines[:3]
		lines[2] += "…"
	}
	return strings.Join(lines, "\n")
}

// kindColor maps a node kind to a saturated fill that stays legible under white
// label text.
func kindColor(k knowledge.Kind) string {
	switch k {
	case knowledge.KindProfile:
		return "#b8860b" // dark goldenrod — the user
	case knowledge.KindProject:
		return "#3b6ea5" // blue
	case knowledge.KindTopic:
		return "#2e8b57" // green
	case knowledge.KindSkill:
		return "#7d5ba6" // purple
	case knowledge.KindTag:
		return "#6b6b7b" // (tags are excluded from the view, but keep a color)
	default:
		return "#4a4a52" // fact — slate
	}
}
