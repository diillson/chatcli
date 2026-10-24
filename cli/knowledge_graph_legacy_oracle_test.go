/*
 * ChatCLI - Legacy graph builder ORACLE (frozen copy, test-only)
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * A verbatim copy of the knowledge-graph derivation as shipped up to
 * v1.224.0 (identifiers prefixed "legacy"). It must never be edited: it is
 * the reference the migration test compares the typed-link builder against,
 * proving every node and every edge users had before still exists with at
 * least the same weight (knowledge_graph_migration_test.go).
 */
package cli

import (
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/diillson/chatcli/cli/board"
	"github.com/diillson/chatcli/cli/ctxmgr"
	"github.com/diillson/chatcli/pkg/knowledge"
)

// legacyGraphBuildState carries the lookup maps the memory-node pass builds so the
// episode/session passes can join against them without recomputation.
type legacyGraphBuildState struct {
	projByPath map[string]string   // workspace dir path → project node id
	projByName map[string]string   // project name (slug) → project node id
	factsByDay map[string][]string // sourceProject+"|"+YYYY-MM-DD → fact node ids
	episByDay  map[string][]string // YYYY-MM-DD → episode node ids
}

// legacyBuildKnowledgeGraph assembles the graph from the live stores. Safe to call
// with a nil memory store or persona handler — it simply yields a smaller graph.
func (cli *ChatCLI) legacyBuildKnowledgeGraph() *knowledge.Graph {
	g := knowledge.New()
	st := cli.legacyAddMemoryNodes(g)
	cli.legacyAddEpisodeNodes(g, st)
	cli.legacyAddSessionNodes(g, st)
	cli.legacyAddKBNodes(g)
	cli.legacyAddSkillNodes(g)
	legacyAddBoardNodes(g)
	legacyLinkWikilinks(g)
	return g
}

// legacyAddBoardNodes joins the work board ("card:" + Card.ID). The board used to
// be a durable-on-disk silo no memory surface could see — a card left in
// doing at the end of a session was invisible to the next one. Nodes carry
// the column in the title so the index-card tally plus one neighbors call
// tells the model what is in flight; @board show has the rest. Edges:
// card↔tag(assignee) (1.0) so squad cards cluster by worker.
func legacyAddBoardNodes(g *knowledge.Graph) {
	cards, err := boardStore().List("")
	if err != nil {
		return
	}
	for _, c := range cards {
		id := "card:" + c.ID
		weight := 1.0
		if c.Column == board.ColDoing {
			weight = 2.0
		}
		g.AddNode(knowledge.Node{
			ID: id, Kind: knowledge.KindCard,
			Title:   "[" + string(c.Column) + "] " + legacyGraphTitle(c.Title),
			Summary: c.Description,
			Weight:  weight,
		})
		if c.Assignee != "" {
			legacyAddTag(g, c.Assignee)
			g.AddEdge(id, legacyTagID(c.Assignee), 1)
		}
	}
}

func (cli *ChatCLI) legacyAddMemoryNodes(g *knowledge.Graph) *legacyGraphBuildState {
	st := &legacyGraphBuildState{
		projByPath: make(map[string]string),
		projByName: make(map[string]string),
		factsByDay: make(map[string][]string),
		episByDay:  make(map[string][]string),
	}
	if cli.memoryStore == nil {
		return st
	}
	m := cli.memoryStore.Manager()

	// Projects first, so fact→project edges can resolve by path.
	for _, p := range m.Projects.GetAll() {
		id := "project:" + legacyGraphSlug(p.Name)
		g.AddNode(knowledge.Node{ID: id, Kind: knowledge.KindProject, Title: p.Name, Summary: p.Description, Weight: float64(p.Priority)})
		st.projByName[legacyGraphSlug(p.Name)] = id
		if p.Path != "" {
			st.projByPath[p.Path] = id
		}
		for _, tech := range p.Technologies {
			legacyAddTag(g, tech)
			g.AddEdge(id, legacyTagID(tech), 1)
		}
	}

	// Facts, with their tags, category and source project.
	factByID := make(map[string]string)
	for _, f := range m.Facts.GetAll() {
		id := "fact:" + f.ID
		g.AddNode(knowledge.Node{ID: id, Kind: knowledge.KindFact, Title: legacyGraphTitle(f.Content), Summary: f.Content, Weight: f.Score})
		factByID[f.ID] = id
		if f.SourceProject != "" {
			day := f.CreatedAt.Format("2006-01-02")
			key := f.SourceProject + "|" + day
			st.factsByDay[key] = append(st.factsByDay[key], id)
		}
		for _, tag := range f.Tags {
			legacyAddTag(g, tag)
			g.AddEdge(id, legacyTagID(tag), 1)
		}
		if f.Category != "" {
			legacyAddTag(g, f.Category)
			g.AddEdge(id, legacyTagID(f.Category), 0.5)
		}
		if pid, ok := st.projByPath[f.SourceProject]; ok {
			g.AddEdge(id, pid, 2)
		}
	}

	// Topics link to their related facts (the relationship already recorded).
	for _, tp := range m.Topics.GetAll() {
		id := "topic:" + legacyGraphSlug(tp.Name)
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
			legacyAddTag(g, sk)
			g.AddEdge("profile:user", legacyTagID(sk), 1)
		}
		for _, goal := range prof.Goals {
			legacyAddTag(g, goal)
			g.AddEdge("profile:user", legacyTagID(goal), 1)
		}
	}
	return st
}

// legacyGraphMaxEpisodeNodes bounds how many recent episodes enter the graph — the
// store caps at 2000 and a full year of timeline would drown the hubs.
const legacyGraphMaxEpisodeNodes = 300

// legacyAddEpisodeNodes joins the episodic timeline into the graph. Edges:
// episode↔project (2.0) via the workspace-path keyspace both stores share
// (Episode.Project IS a workspace dir, same as Fact.SourceProject), and
// episode↔fact (1.0) for facts learned in the same project on the same day —
// the durable "what happened while this was learned" bridge. Wikilinks in
// episode summaries come free from legacyLinkWikilinks.
func (cli *ChatCLI) legacyAddEpisodeNodes(g *knowledge.Graph, st *legacyGraphBuildState) {
	if cli.memoryStore == nil {
		return
	}
	m := cli.memoryStore.Manager()
	for _, e := range m.Episodes.Range(zeroTime, zeroTime, "", "", legacyGraphMaxEpisodeNodes) {
		day := e.Date.Format("2006-01-02")
		id := "episode:" + e.ID
		summary := e.Summary
		if o := strings.TrimSpace(e.Outcome); o != "" {
			summary += " → " + o
		}
		g.AddNode(knowledge.Node{
			ID: id, Kind: knowledge.KindEpisode,
			Title:   day + " " + legacyGraphTitle(e.Summary),
			Summary: summary,
		})
		st.episByDay[day] = append(st.episByDay[day], id)
		if pid, ok := st.projByPath[e.Project]; ok {
			g.AddEdge(id, pid, 2)
		}
		for _, fid := range st.factsByDay[e.Project+"|"+day] {
			g.AddEdge(id, fid, 1)
		}
	}
}

// legacyGraphMaxSessionNodes bounds saved-session nodes to the most recent by
// mtime — with autosaves the store holds hundreds.
const legacyGraphMaxSessionNodes = 50

// legacyAddSessionNodes joins saved sessions via a LIGHTWEIGHT listing (name +
// mtime stat only — never the search corpus, whose first build parses every
// session JSON and would tax chat turns). Titles are enriched from the
// corpus only when it is already warm. The session NAME is the identity;
// renames orphan a node until the fingerprint TTL heals the derived cache.
// Edges: session↔project (1.5) when the project name appears in the session
// name/title, session↔episode (1.0) same calendar day.
func (cli *ChatCLI) legacyAddSessionNodes(g *knowledge.Graph, st *legacyGraphBuildState) {
	if cli.sessionManager == nil {
		return
	}
	type sess struct {
		name  string
		mtime int64
		day   string
	}
	entries, err := os.ReadDir(cli.sessionManager.sessionsDir)
	if err != nil {
		return
	}
	titles := cli.sessionManager.warmTitles()
	all := make([]sess, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		all = append(all, sess{
			name:  strings.TrimSuffix(e.Name(), ".json"),
			mtime: info.ModTime().UnixNano(),
			day:   info.ModTime().Format("2006-01-02"),
		})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].mtime > all[j].mtime })
	if len(all) > legacyGraphMaxSessionNodes {
		all = all[:legacyGraphMaxSessionNodes]
	}
	for _, s := range all {
		id := "session:" + s.name
		title := s.name
		if t := strings.TrimSpace(titles[s.name]); t != "" {
			title = t
		}
		g.AddNode(knowledge.Node{ID: id, Kind: knowledge.KindSession, Title: title, Summary: ""})
		hay := legacyGraphSlug(s.name + " " + title)
		for slug, pid := range st.projByName {
			if slug != "" && strings.Contains(hay, slug) {
				g.AddEdge(id, pid, 1.5)
			}
		}
		for _, eid := range st.episByDay[s.day] {
			g.AddEdge(id, eid, 1)
		}
	}
}

// legacyAddKBNodes joins knowledge-mode contexts at CONTEXT granularity ("kb:" +
// FileContext.ID — chunk-level nodes would put tens of thousands of vertices
// in the graph). Edges: kb↔tag (1.0) from the context's tags.
func (cli *ChatCLI) legacyAddKBNodes(g *knowledge.Graph) {
	if cli.contextHandler == nil {
		return
	}
	mgr := cli.contextHandler.GetManager()
	if mgr == nil {
		return
	}
	ctxs, err := mgr.ListContexts(nil)
	if err != nil {
		return
	}
	for _, fc := range ctxs {
		if fc == nil || fc.Mode != ctxmgr.ModeKnowledge {
			continue
		}
		id := "kb:" + fc.ID
		g.AddNode(knowledge.Node{ID: id, Kind: knowledge.KindKB, Title: fc.Name, Summary: fc.Description})
		for _, tag := range fc.Tags {
			legacyAddTag(g, tag)
			g.AddEdge(id, legacyTagID(tag), 1)
		}
	}
}

func (cli *ChatCLI) legacyAddSkillNodes(g *knowledge.Graph) {
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
			slug := legacyGraphSlug(tr)
			// Connect to a topic of the same slug if one exists (no-op otherwise).
			g.AddEdge(id, "topic:"+slug, 1)
			legacyAddTag(g, tr)
			g.AddEdge(id, legacyTagID(tr), 0.5)
		}
	}
}

var legacyWikilinkRe = regexp.MustCompile(`\[\[([^\]]+)\]\]`)

// legacyLinkWikilinks wires edges from any [[Title]] reference found in a node's
// summary to the node bearing that title — the backbone of an Obsidian vault.
func legacyLinkWikilinks(g *knowledge.Graph) {
	byTitle := make(map[string]string)
	for _, n := range g.Nodes() {
		if t := strings.ToLower(strings.TrimSpace(n.Title)); t != "" {
			if _, exists := byTitle[t]; !exists {
				byTitle[t] = n.ID
			}
		}
	}
	for _, n := range g.Nodes() {
		for _, match := range legacyWikilinkRe.FindAllStringSubmatch(n.Summary, -1) {
			target := strings.ToLower(strings.TrimSpace(match[1]))
			if tid, ok := byTitle[target]; ok {
				g.AddEdge(n.ID, tid, 1.5)
			}
		}
	}
}

// --- helpers ---

func legacyGraphSlug(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func legacyTagID(s string) string { return "tag:" + legacyGraphSlug(s) }

func legacyAddTag(g *knowledge.Graph, label string) {
	label = strings.TrimSpace(label)
	if label == "" {
		return
	}
	g.AddNode(knowledge.Node{ID: legacyTagID(label), Kind: knowledge.KindTag, Title: label})
}

// legacyGraphTitle turns a fact's content into a short node title (first line, capped).
func legacyGraphTitle(content string) string {
	line := strings.TrimSpace(content)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = strings.TrimSpace(line[:i])
	}
	return truncateForLog(line, 60)
}
