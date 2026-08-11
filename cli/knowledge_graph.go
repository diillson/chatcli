/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * knowledge_graph.go — derives the in-core knowledge graph from the existing
 * memory, skill, session and context stores, and implements the @graph tool's
 * adapter.
 *
 * The graph is a DERIVED index: nodes and edges are computed from data
 * already on disk. Edges come from the relationships those stores ALREADY
 * record — topic↔fact links, a fact's source project, an episode's project
 * and date, shared tags, a skill's triggers — plus [[wikilinks]] parsed from
 * note text.
 *
 * With CHATCLI_MEMORY_GRAPH on (default), the derivation is served through
 * the persisted cache in cli/workspace/memory/graph_cache.go: consumers call
 * cli.knowledgeGraph() and get an immutable snapshot that only rebuilds when
 * a source store changed (dirty taps + fingerprint TTL), with graph.json
 * carrying it across boots. Off, every call derives from scratch — the
 * legacy behavior, byte-identical.
 */
package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/board"
	"github.com/diillson/chatcli/cli/ctxmgr"
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/pkg/knowledge"
)

// zeroTime is the open bound for EpisodeStore.Range queries.
var zeroTime time.Time

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

// memoryGraphEnabled reports whether the persisted graph cache serves the
// consumers (default on). Off restores the legacy derive-per-call behavior.
// Distinct from CHATCLI_GRAPH_INDEX on purpose: that env gates the per-turn
// card payload, this one gates the cache + recall expansion — "card off,
// expansion on" is a legitimate lean-prompt setup.
func memoryGraphEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(config.MemoryGraphEnv))) {
	case "off", "false", "0", "no", "disabled":
		return false
	}
	return true
}

// knowledgeGraph is the single accessor every graph consumer goes through:
// the cached snapshot when the persisted-graph feature is live, else a fresh
// derivation (feature off, or the cache was never wired — e.g. no memory
// store). Callers must treat the returned graph as read-only.
func (cli *ChatCLI) knowledgeGraph() *knowledge.Graph {
	if memoryGraphEnabled() && cli.memoryStore != nil {
		if g := cli.memoryStore.Manager().KnowledgeGraph(); g != nil {
			return g
		}
	}
	return cli.buildKnowledgeGraph()
}

// markGraphDirty flags the persisted graph cache stale after a CLI-side
// mutation the memory stores cannot observe (session saved, skill installed,
// context created/deleted). Nil-safe no-op when the cache is not wired.
func (cli *ChatCLI) markGraphDirty() {
	if cli.memoryStore != nil {
		cli.memoryStore.Manager().MarkGraphDirty()
	}
}

// graphIndexBlock renders the prompt block: the deterministic MOC card plus the
// pull hint. Returns "" when disabled or the graph is empty, so quiet setups pay
// nothing. The card itself is byte-stable between knowledge changes, so it does
// not bust the prompt cache turn to turn. Routed through the memory threat
// scanner — fact/topic titles flow into the hub list, and every other memory
// injection is sanitized the same way.
func (cli *ChatCLI) graphIndexBlock() string {
	if !graphIndexEnabled() {
		return ""
	}
	card := cli.knowledgeGraph().IndexCard(graphIndexMaxHubs)
	if card == "" {
		return ""
	}
	card = cli.memoryStore.SanitizeForPrompt(card)
	return "# Knowledge Graph (index)\n" + card + "\n" + graphRecallHint
}

// graphBuildState carries the lookup maps the memory-node pass builds so the
// episode/session passes can join against them without recomputation.
type graphBuildState struct {
	projByPath map[string]string   // workspace dir path → project node id
	projByName map[string]string   // project name (slug) → project node id
	factsByDay map[string][]string // sourceProject+"|"+YYYY-MM-DD → fact node ids
	episByDay  map[string][]string // YYYY-MM-DD → episode node ids
}

// buildKnowledgeGraph assembles the graph from the live stores. Safe to call
// with a nil memory store or persona handler — it simply yields a smaller graph.
func (cli *ChatCLI) buildKnowledgeGraph() *knowledge.Graph {
	g := knowledge.New()
	st := cli.addMemoryNodes(g)
	cli.addEpisodeNodes(g, st)
	cli.addSessionNodes(g, st)
	cli.addKBNodes(g)
	cli.addSkillNodes(g)
	addBoardNodes(g)
	linkWikilinks(g)
	return g
}

// addBoardNodes joins the work board ("card:" + Card.ID). The board used to
// be a durable-on-disk silo no memory surface could see — a card left in
// doing at the end of a session was invisible to the next one. Nodes carry
// the column in the title so the index-card tally plus one neighbors call
// tells the model what is in flight; @board show has the rest. Edges:
// card↔tag(assignee) (1.0) so squad cards cluster by worker.
func addBoardNodes(g *knowledge.Graph) {
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
			Title:   "[" + string(c.Column) + "] " + graphTitle(c.Title),
			Summary: c.Description,
			Weight:  weight,
		})
		if c.Assignee != "" {
			addTag(g, c.Assignee)
			g.AddEdge(id, tagID(c.Assignee), 1)
		}
	}
}

func (cli *ChatCLI) addMemoryNodes(g *knowledge.Graph) *graphBuildState {
	st := &graphBuildState{
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
		id := "project:" + graphSlug(p.Name)
		g.AddNode(knowledge.Node{ID: id, Kind: knowledge.KindProject, Title: p.Name, Summary: p.Description, Weight: float64(p.Priority)})
		st.projByName[graphSlug(p.Name)] = id
		if p.Path != "" {
			st.projByPath[p.Path] = id
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
		if f.SourceProject != "" {
			day := f.CreatedAt.Format("2006-01-02")
			key := f.SourceProject + "|" + day
			st.factsByDay[key] = append(st.factsByDay[key], id)
		}
		for _, tag := range f.Tags {
			addTag(g, tag)
			g.AddEdge(id, tagID(tag), 1)
		}
		if f.Category != "" {
			addTag(g, f.Category)
			g.AddEdge(id, tagID(f.Category), 0.5)
		}
		if pid, ok := st.projByPath[f.SourceProject]; ok {
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
	return st
}

// graphMaxEpisodeNodes bounds how many recent episodes enter the graph — the
// store caps at 2000 and a full year of timeline would drown the hubs.
const graphMaxEpisodeNodes = 300

// addEpisodeNodes joins the episodic timeline into the graph. Edges:
// episode↔project (2.0) via the workspace-path keyspace both stores share
// (Episode.Project IS a workspace dir, same as Fact.SourceProject), and
// episode↔fact (1.0) for facts learned in the same project on the same day —
// the durable "what happened while this was learned" bridge. Wikilinks in
// episode summaries come free from linkWikilinks.
func (cli *ChatCLI) addEpisodeNodes(g *knowledge.Graph, st *graphBuildState) {
	if cli.memoryStore == nil {
		return
	}
	m := cli.memoryStore.Manager()
	for _, e := range m.Episodes.Range(zeroTime, zeroTime, "", "", graphMaxEpisodeNodes) {
		day := e.Date.Format("2006-01-02")
		id := "episode:" + e.ID
		summary := e.Summary
		if o := strings.TrimSpace(e.Outcome); o != "" {
			summary += " → " + o
		}
		g.AddNode(knowledge.Node{
			ID: id, Kind: knowledge.KindEpisode,
			Title:   day + " " + graphTitle(e.Summary),
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

// graphMaxSessionNodes bounds saved-session nodes to the most recent by
// mtime — with autosaves the store holds hundreds.
const graphMaxSessionNodes = 50

// addSessionNodes joins saved sessions via a LIGHTWEIGHT listing (name +
// mtime stat only — never the search corpus, whose first build parses every
// session JSON and would tax chat turns). Titles are enriched from the
// corpus only when it is already warm. The session NAME is the identity;
// renames orphan a node until the fingerprint TTL heals the derived cache.
// Edges: session↔project (1.5) when the project name appears in the session
// name/title, session↔episode (1.0) same calendar day.
func (cli *ChatCLI) addSessionNodes(g *knowledge.Graph, st *graphBuildState) {
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
	if len(all) > graphMaxSessionNodes {
		all = all[:graphMaxSessionNodes]
	}
	for _, s := range all {
		id := "session:" + s.name
		title := s.name
		if t := strings.TrimSpace(titles[s.name]); t != "" {
			title = t
		}
		g.AddNode(knowledge.Node{ID: id, Kind: knowledge.KindSession, Title: title, Summary: ""})
		hay := graphSlug(s.name + " " + title)
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

// addKBNodes joins knowledge-mode contexts at CONTEXT granularity ("kb:" +
// FileContext.ID — chunk-level nodes would put tens of thousands of vertices
// in the graph). Edges: kb↔tag (1.0) from the context's tags.
func (cli *ChatCLI) addKBNodes(g *knowledge.Graph) {
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
			addTag(g, tag)
			g.AddEdge(id, tagID(tag), 1)
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
	card := cli.knowledgeGraph().IndexCard(graphIndexMaxHubs)
	if card == "" {
		return "The knowledge graph is empty so far.", nil
	}
	return cli.memoryStore.SanitizeForPrompt(card), nil
}

// graphNeighborsText renders the local graph (backlinks + related notes) of the
// node best matching idOrQuery, for @memory neighbors. Free text resolves to the
// best-matching node, so the model can pass a subject rather than a node id.
func (cli *ChatCLI) graphNeighborsText(idOrQuery string) (string, error) {
	g := cli.knowledgeGraph()

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
		return cli.memoryStore.SanitizeForPrompt(b.String()), nil
	}
	b.WriteString("Connected:\n")
	for _, n := range hood {
		writeGraphNode(&b, n)
	}
	// Fact/topic summaries flow verbatim into the conversation — same
	// threatscan pass every other memory injection gets.
	return cli.memoryStore.SanitizeForPrompt(strings.TrimRight(b.String(), "\n")), nil
}

// --- persisted-cache wiring (source builder + fingerprint) ---

// graphFingerprint hashes stat lines (name, size, mtime) of every source the
// derivation reads — memory JSONs, skill dirs, session files, context store.
// Stat-only by design: tens to low hundreds of syscalls, no parsing, run at
// boot and once per fingerprint TTL. Any change flips the hash and the cache
// rebuilds.
func (cli *ChatCLI) graphFingerprint() string {
	h := sha256.New()
	statFile := func(path string) {
		if fi, err := os.Stat(path); err == nil {
			fmt.Fprintf(h, "%s|%d|%d\n", path, fi.Size(), fi.ModTime().UnixNano())
		}
	}
	statDir := func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if info, err := e.Info(); err == nil {
				fmt.Fprintf(h, "%s/%s|%d|%d\n", dir, e.Name(), info.Size(), info.ModTime().UnixNano())
			}
		}
	}
	if cli.memoryStore != nil {
		md := cli.memoryStore.Manager().MemoryDir()
		for _, f := range []string{
			"memory_index.json", "topics.json", "projects.json",
			"user_profile.json", "episodes.json",
		} {
			statFile(filepath.Join(md, f))
		}
	}
	if cli.personaHandler != nil {
		if mgr := cli.personaHandler.GetManager(); mgr != nil {
			for _, d := range mgr.SkillDirs() {
				statDir(d)
			}
		}
	}
	if cli.sessionManager != nil {
		statDir(cli.sessionManager.sessionsDir)
	}
	if cli.contextHandler != nil {
		if mgr := cli.contextHandler.GetManager(); mgr != nil && mgr.Storage != nil {
			statDir(mgr.Storage.GetStoragePath())
		}
	}
	statFile(board.DefaultPath())
	return hex.EncodeToString(h.Sum(nil))
}

// wireMemoryGraph attaches the graph derivation to the persisted cache.
// Called once at startup after every source store exists; a no-op when the
// feature is disabled (consumers then fall back to derive-per-call).
func (cli *ChatCLI) wireMemoryGraph() {
	if !memoryGraphEnabled() || cli.memoryStore == nil {
		return
	}
	cli.memoryStore.Manager().SetGraphSource(cli.buildKnowledgeGraph, cli.graphFingerprint)
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
