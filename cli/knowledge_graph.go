/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * knowledge_graph.go — derives the in-core knowledge graph from the existing
 * memory and skill stores, and implements the @graph tool's adapter.
 *
 * Nothing new is persisted: nodes and edges are computed on demand from facts,
 * topics, projects, profile and skills already on disk. Edges come from the
 * relationships those stores ALREADY record — topic↔fact links, a fact's source
 * project, shared tags, a skill's triggers — plus [[wikilinks]] parsed from
 * note text. This honors the index/pull discipline: the graph is a retrieval
 * structure, built fresh per query, never duplicated storage.
 */
package cli

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/pkg/knowledge"
)

// graphRecallHint points the model at the pull tool. It is intentionally short
// and stable so the injected block stays prompt-cache friendly.
const graphRecallHint = "To go deeper, call @memory neighbors <subject> for a subject's connected notes (backlinks + related)."

// graphIndexEnabled reports whether the per-turn map-of-content card is injected.
// Default on; any falsey value disables it. The @graph pull tool is unaffected.
func graphIndexEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(config.GraphIndexEnv))) {
	case "off", "false", "0", "no", "disabled":
		return false
	}
	return true
}

// graphIndexBlock renders the prompt block: the deterministic MOC card plus the
// pull hint. Returns "" when disabled or the graph is empty, so quiet setups pay
// nothing. The card itself is byte-stable between knowledge changes, so it does
// not bust the prompt cache turn to turn.
func (cli *ChatCLI) graphIndexBlock() string {
	if !graphIndexEnabled() {
		return ""
	}
	card := cli.buildKnowledgeGraph().IndexCard(graphIndexMaxHubs)
	if card == "" {
		return ""
	}
	return "# Knowledge Graph (index)\n" + card + "\n" + graphRecallHint
}

// buildKnowledgeGraph assembles the graph from the live stores. Safe to call
// with a nil memory store or persona handler — it simply yields a smaller graph.
func (cli *ChatCLI) buildKnowledgeGraph() *knowledge.Graph {
	g := knowledge.New()
	cli.addMemoryNodes(g)
	cli.addSkillNodes(g)
	linkWikilinks(g)
	return g
}

func (cli *ChatCLI) addMemoryNodes(g *knowledge.Graph) {
	if cli.memoryStore == nil {
		return
	}
	m := cli.memoryStore.Manager()

	// Projects first, so fact→project edges can resolve by path.
	projByPath := make(map[string]string)
	for _, p := range m.Projects.GetAll() {
		id := "project:" + graphSlug(p.Name)
		g.AddNode(knowledge.Node{ID: id, Kind: knowledge.KindProject, Title: p.Name, Summary: p.Description, Weight: float64(p.Priority)})
		if p.Path != "" {
			projByPath[p.Path] = id
		}
		for _, tech := range p.Technologies {
			addTag(g, tech)
			g.AddEdge(id, tagID(tech), 1)
		}
	}

	// Facts, with their tags, category and source project.
	factByID := make(map[string]string)
	for _, f := range m.Facts.GetAll() {
		id := "fact:" + f.ID
		g.AddNode(knowledge.Node{ID: id, Kind: knowledge.KindFact, Title: graphTitle(f.Content), Summary: f.Content, Weight: f.Score})
		factByID[f.ID] = id
		for _, tag := range f.Tags {
			addTag(g, tag)
			g.AddEdge(id, tagID(tag), 1)
		}
		if f.Category != "" {
			addTag(g, f.Category)
			g.AddEdge(id, tagID(f.Category), 0.5)
		}
		if pid, ok := projByPath[f.SourceProject]; ok {
			g.AddEdge(id, pid, 2)
		}
	}

	// Topics link to their related facts (the relationship already recorded).
	for _, tp := range m.Topics.GetAll() {
		id := "topic:" + graphSlug(tp.Name)
		g.AddNode(knowledge.Node{ID: id, Kind: knowledge.KindTopic, Title: tp.Name, Summary: tp.Summary, Weight: float64(tp.Mentions)})
		for _, fid := range tp.RelatedFacts {
			if nfid, ok := factByID[fid]; ok {
				g.AddEdge(id, nfid, 2)
			}
		}
	}

	// The user node, linked to declared skills and goals.
	if !m.Profile.IsEmpty() {
		prof := m.Profile.Get()
		name := prof.Name
		if name == "" {
			name = "user"
		}
		g.AddNode(knowledge.Node{ID: "profile:user", Kind: knowledge.KindProfile, Title: name, Summary: prof.Role, Weight: 10})
		for _, sk := range prof.Skills {
			addTag(g, sk)
			g.AddEdge("profile:user", tagID(sk), 1)
		}
		for _, goal := range prof.Goals {
			addTag(g, goal)
			g.AddEdge("profile:user", tagID(goal), 1)
		}
	}
}

func (cli *ChatCLI) addSkillNodes(g *knowledge.Graph) {
	if cli.personaHandler == nil {
		return
	}
	skills, err := cli.personaHandler.GetManager().ListSkills()
	if err != nil {
		return
	}
	for _, s := range skills {
		if s == nil || s.Name == "" {
			continue
		}
		id := "skill:" + s.Name
		g.AddNode(knowledge.Node{ID: id, Kind: knowledge.KindSkill, Title: s.Name, Summary: s.Description, Weight: 1})
		for _, tr := range s.Triggers {
			slug := graphSlug(tr)
			// Connect to a topic of the same slug if one exists (no-op otherwise).
			g.AddEdge(id, "topic:"+slug, 1)
			addTag(g, tr)
			g.AddEdge(id, tagID(tr), 0.5)
		}
	}
}

var wikilinkRe = regexp.MustCompile(`\[\[([^\]]+)\]\]`)

// linkWikilinks wires edges from any [[Title]] reference found in a node's
// summary to the node bearing that title — the backbone of an Obsidian vault.
func linkWikilinks(g *knowledge.Graph) {
	byTitle := make(map[string]string)
	for _, n := range g.Nodes() {
		if t := strings.ToLower(strings.TrimSpace(n.Title)); t != "" {
			if _, exists := byTitle[t]; !exists {
				byTitle[t] = n.ID
			}
		}
	}
	for _, n := range g.Nodes() {
		for _, match := range wikilinkRe.FindAllStringSubmatch(n.Summary, -1) {
			target := strings.ToLower(strings.TrimSpace(match[1]))
			if tid, ok := byTitle[target]; ok {
				g.AddEdge(n.ID, tid, 1.5)
			}
		}
	}
}

// --- helpers ---

func graphSlug(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func tagID(s string) string { return "tag:" + graphSlug(s) }

func addTag(g *knowledge.Graph, label string) {
	label = strings.TrimSpace(label)
	if label == "" {
		return
	}
	g.AddNode(knowledge.Node{ID: tagID(label), Kind: knowledge.KindTag, Title: label})
}

// graphTitle turns a fact's content into a short node title (first line, capped).
func graphTitle(content string) string {
	line := strings.TrimSpace(content)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = strings.TrimSpace(line[:i])
	}
	return truncateForLog(line, 60)
}

// --- graph access (exposed through the @memory tool: map / neighbors) ---

const (
	graphHoodHops     = 2
	graphHoodLimit    = 12
	graphIndexMaxHubs = 8
)

// graphMapText renders the graph map-of-content (counts + hubs) for @memory map.
func (cli *ChatCLI) graphMapText() (string, error) {
	card := cli.buildKnowledgeGraph().IndexCard(graphIndexMaxHubs)
	if card == "" {
		return "The knowledge graph is empty so far.", nil
	}
	return card, nil
}

// graphNeighborsText renders the local graph (backlinks + related notes) of the
// node best matching idOrQuery, for @memory neighbors. Free text resolves to the
// best-matching node, so the model can pass a subject rather than a node id.
func (cli *ChatCLI) graphNeighborsText(idOrQuery string) (string, error) {
	g := cli.buildKnowledgeGraph()

	seed, ok := g.Node(idOrQuery)
	if !ok {
		if hits := g.Search(strings.Fields(idOrQuery), 1); len(hits) > 0 {
			seed = hits[0]
		}
	}
	if seed == nil {
		return i18n.T("graph.tool.no_node", idOrQuery), nil
	}

	hood := g.Neighborhood(seed.ID, graphHoodHops, graphHoodLimit)
	var b strings.Builder
	fmt.Fprintf(&b, "Local graph of %q (%s):\n", strings.TrimSpace(seed.Title), seed.ID)
	if seed.Summary != "" && seed.Summary != seed.Title {
		fmt.Fprintf(&b, "  · %s\n", truncateForLog(seed.Summary, 200))
	}
	if len(hood) == 0 {
		b.WriteString("  (no connected notes yet)")
		return b.String(), nil
	}
	b.WriteString("Connected:\n")
	for _, n := range hood {
		writeGraphNode(&b, n)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func writeGraphNode(b *strings.Builder, n *knowledge.Node) {
	title := strings.TrimSpace(n.Title)
	if title == "" {
		title = n.ID
	}
	if summary := strings.TrimSpace(n.Summary); summary != "" && summary != title {
		fmt.Fprintf(b, "  - [%s] %s — %s\n", n.Kind, title, truncateForLog(summary, 160))
	} else {
		fmt.Fprintf(b, "  - [%s] %s\n", n.Kind, title)
	}
}
