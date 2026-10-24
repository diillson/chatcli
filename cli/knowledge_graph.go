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
 * The graph is the vault model of everything chatcli knows: every fact,
 * topic, project, episode, session, skill, board card, context and context
 * DOCUMENT is a note, and notes connect through typed, directed links
 * (pkg/knowledge/links.go) — so backlinks exist and every link says why.
 *
 * Nodes and most links are DERIVED from data already on disk: the
 * relationships the stores ALREADY record (topic↔fact, a fact's source
 * project, an episode's project and date, shared tags, a skill's triggers),
 * the links documents write to each other, [[wikilinks]] in any note, and
 * autolinked mentions of note names in prose. The one non-derived input is
 * links.json: explicit links the user drew (@memory link, /memory link).
 * Every adjacency weight the untyped graph had is preserved exactly; the
 * migration test proves the new graph is a superset of the legacy one.
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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/board"
	"github.com/diillson/chatcli/cli/ctxmgr"
	"github.com/diillson/chatcli/cli/workspace"
	"github.com/diillson/chatcli/cli/workspace/memory"
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
	// supersededBy maps a superseded fact id to the fact that replaced it
	// (from the "(supersedes <id>)" provenance note), so explicit links
	// drawn to an old fact follow it to its successor.
	supersededBy map[string]string
	// docWikilinks are the [[targets]] documents wrote that name nothing in
	// their own context, resolved vault-wide after every node exists.
	docWikilinks map[string][]string
	// factsByDate indexes fact nodes by creation day alone (factsByDay is
	// keyed by project too), for the daily-note join.
	factsByDate map[string][]string
}

// graphSources is the fixed store set one knowledge graph derives from.
// Binding it once — at wiring time, per tenant — instead of reading the
// ChatCLI's fields at build time is what keeps a background rebuild (the
// graph cache, the vault syncer) from ever mixing two tenants' stores: the
// gateway swaps those fields per turn.
type graphSources struct {
	memoryStore    *workspace.MemoryStore
	sessionManager *SessionManager
	contextHandler *ContextHandler
	personaHandler *PersonaHandler
}

// graphSources snapshots the store set currently installed on the ChatCLI.
func (cli *ChatCLI) graphSources() *graphSources {
	return &graphSources{
		memoryStore:    cli.memoryStore,
		sessionManager: cli.sessionManager,
		contextHandler: cli.contextHandler,
		personaHandler: cli.personaHandler,
	}
}

// buildKnowledgeGraph assembles the graph from the currently installed
// stores (see graphSources.build).
func (cli *ChatCLI) buildKnowledgeGraph() *knowledge.Graph {
	return cli.graphSources().build()
}

// build assembles the graph from the bound stores. Safe with any of them
// nil — it simply yields a smaller graph.
//
// Order matters: every node first, then the text-driven passes. Explicit
// links precede wikilinks and mentions, so an autolinked mention never
// restates a relationship the user (or a store) already recorded.
func (src *graphSources) build() *knowledge.Graph {
	g := knowledge.New()
	st := src.addMemoryNodes(g)
	src.addEpisodeNodes(g, st)
	src.addJournalNodes(g, st)
	src.addSessionNodes(g, st)
	src.addContextNodes(g, st)
	src.addSkillNodes(g)
	addBoardNodes(g)
	src.addStoredLinks(g, st)
	g.LinkWikilinks()
	linkDocWikilinks(g, st)
	g.LinkMentions(graphMentionOptions)
	g.Freeze()
	return g
}

// knowledgeGraph is the bound-store twin of ChatCLI.knowledgeGraph: the
// cached snapshot when the persisted graph is live, else a fresh build.
func (src *graphSources) knowledgeGraph() *knowledge.Graph {
	if memoryGraphEnabled() && src.memoryStore != nil {
		if g := src.memoryStore.Manager().KnowledgeGraph(); g != nil {
			return g
		}
	}
	return src.build()
}

// graphMentionOptions: prose in facts, topics, episodes, projects and cards
// autolinks to the notes people name — projects, topics, skills and
// contexts. Documents and tags are not mention targets (file names and tag
// words are too generic to find in prose reliably).
var graphMentionOptions = knowledge.MentionOptions{
	SourceKinds: []knowledge.Kind{
		knowledge.KindFact, knowledge.KindTopic, knowledge.KindEpisode,
		knowledge.KindProject, knowledge.KindCard, knowledge.KindNote,
	},
	TargetKinds: []knowledge.Kind{
		knowledge.KindProject, knowledge.KindTopic, knowledge.KindSkill,
		knowledge.KindKB, knowledge.KindContext,
	},
	MinRunes:     3,
	MaxPerSource: 8,
	Weight:       1,
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
		g.AddAlias(id, c.Title)
		if c.Assignee != "" {
			addTag(g, c.Assignee)
			g.AddLink(knowledge.Link{From: id, To: tagID(c.Assignee), Rel: knowledge.RelAssigned, Weight: 1})
		}
	}
}

// addMemoryNodes joins the memory stores: projects, facts, topics and the
// user profile, with the relationships those stores already record.
func (src *graphSources) addMemoryNodes(g *knowledge.Graph) *graphBuildState {
	st := &graphBuildState{
		projByPath:   make(map[string]string),
		projByName:   make(map[string]string),
		factsByDay:   make(map[string][]string),
		episByDay:    make(map[string][]string),
		supersededBy: make(map[string]string),
		docWikilinks: make(map[string][]string),
		factsByDate:  make(map[string][]string),
	}
	if src.memoryStore == nil {
		return st
	}
	m := src.memoryStore.Manager()

	// Projects first, so fact→project edges can resolve by path.
	for _, p := range m.Projects.GetAll() {
		id := "project:" + graphSlug(p.Name)
		g.AddNode(knowledge.Node{ID: id, Kind: knowledge.KindProject, Title: p.Name, Summary: p.Description, Weight: float64(p.Priority)})
		st.projByName[graphSlug(p.Name)] = id
		if p.Path != "" {
			st.projByPath[p.Path] = id
			g.AddAlias(id, filepath.Base(p.Path))
		}
		for _, tech := range p.Technologies {
			addTag(g, tech)
			g.AddLink(knowledge.Link{From: id, To: tagID(tech), Rel: knowledge.RelUses, Weight: 1})
		}
	}

	// Facts, with their tags, category and source project.
	factByID := make(map[string]string)
	for _, f := range m.Facts.GetAll() {
		id := "fact:" + f.ID
		g.AddNode(knowledge.Node{ID: id, Kind: knowledge.KindFact, Title: graphTitle(f.Content), Summary: f.Content, Weight: f.Score})
		factByID[f.ID] = id
		if old := supersededFactID(f.Provenance); old != "" && old != f.ID {
			st.supersededBy[old] = f.ID
		}
		if !f.CreatedAt.IsZero() {
			date := f.CreatedAt.Format("2006-01-02")
			st.factsByDate[date] = append(st.factsByDate[date], id)
		}
		if f.SourceProject != "" {
			day := f.CreatedAt.Format("2006-01-02")
			key := f.SourceProject + "|" + day
			st.factsByDay[key] = append(st.factsByDay[key], id)
		}
		for _, tag := range f.Tags {
			addTag(g, tag)
			g.AddLink(knowledge.Link{From: id, To: tagID(tag), Rel: knowledge.RelTagged, Weight: 1})
		}
		if f.Category != "" {
			addTag(g, f.Category)
			g.AddLink(knowledge.Link{From: id, To: tagID(f.Category), Rel: knowledge.RelCategory, Weight: 0.5})
		}
		if pid, ok := st.projByPath[f.SourceProject]; ok {
			g.AddLink(knowledge.Link{From: id, To: pid, Rel: knowledge.RelInProject, Weight: 2})
		}
	}

	// Topics link to their related facts (the relationship already recorded).
	for _, tp := range m.Topics.GetAll() {
		id := "topic:" + graphSlug(tp.Name)
		g.AddNode(knowledge.Node{ID: id, Kind: knowledge.KindTopic, Title: tp.Name, Summary: tp.Summary, Weight: float64(tp.Mentions)})
		for _, fid := range tp.RelatedFacts {
			if nfid, ok := factByID[fid]; ok {
				g.AddLink(knowledge.Link{From: id, To: nfid, Rel: knowledge.RelAbout, Weight: 2})
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
		g.AddAlias("profile:user", "user")
		for _, sk := range prof.Skills {
			addTag(g, sk)
			g.AddLink(knowledge.Link{From: "profile:user", To: tagID(sk), Rel: knowledge.RelHas, Weight: 1})
		}
		for _, goal := range prof.Goals {
			addTag(g, goal)
			g.AddLink(knowledge.Link{From: "profile:user", To: tagID(goal), Rel: knowledge.RelHas, Weight: 1})
		}
	}
	return st
}

// supersedesMarker is the provenance note FactIndex writes on supersession.
const supersedesMarker = "(supersedes "

// supersededFactID extracts the replaced fact id from a provenance note
// ("extraction (supersedes 3f9a…)"); "" when the fact replaced nothing.
func supersededFactID(provenance string) string {
	i := strings.LastIndex(provenance, supersedesMarker)
	if i < 0 {
		return ""
	}
	rest := provenance[i+len(supersedesMarker):]
	if j := strings.IndexByte(rest, ')'); j >= 0 {
		return strings.TrimSpace(rest[:j])
	}
	return ""
}

// graphMaxEpisodeNodes bounds how many recent episodes enter the graph — the
// store caps at 2000 and a full year of timeline would drown the hubs.
const graphMaxEpisodeNodes = 300

// addEpisodeNodes joins the episodic timeline into the graph. Edges:
// episode↔project (2.0) via the workspace-path keyspace both stores share
// (Episode.Project IS a workspace dir, same as Fact.SourceProject), and
// episode↔fact (1.0) for facts learned in the same project on the same day —
// the durable "what happened while this was learned" bridge. Wikilinks in
// episode summaries come free from LinkWikilinks.
func (src *graphSources) addEpisodeNodes(g *knowledge.Graph, st *graphBuildState) {
	if src.memoryStore == nil {
		return
	}
	m := src.memoryStore.Manager()
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
			g.AddLink(knowledge.Link{From: id, To: pid, Rel: knowledge.RelInProject, Weight: 2})
		}
		for _, fid := range st.factsByDay[e.Project+"|"+day] {
			g.AddLink(knowledge.Link{From: id, To: fid, Rel: knowledge.RelSameDay, Weight: 1})
		}
	}
}

const (
	// graphJournalSummaryRunes bounds a journal note's summary in the graph
	// (the vault note carries the full text).
	graphJournalSummaryRunes = 600
	// graphJournalMaxFactLinks bounds the same-day fact links of one daily
	// note — a busy day must not turn its note into a hub of every fact.
	graphJournalMaxFactLinks = 20
)

// addJournalNodes joins the memory journal: daily notes ("note:daily:"),
// weekly and monthly rollup digests ("note:weekly:", "note:monthly:").
// Edges: daily→episode and daily→fact of the same day (1.0), weekly→daily
// notes of its ISO week and monthly→weeklies starting in its month
// (summarizes, 1.0). Mentions in their text autolink like any prose.
func (src *graphSources) addJournalNodes(g *knowledge.Graph, st *graphBuildState) {
	if src.memoryStore == nil {
		return
	}
	m := src.memoryStore.Manager()
	dailyByDate := make(map[string]string)
	if m.Daily != nil {
		for _, dn := range m.Daily.GetRecentDailyNotes(m.GetConfig().DailyNoteRetention) {
			date := dn.Date.Format("2006-01-02")
			id := "note:daily:" + date
			g.AddNode(knowledge.Node{ID: id, Kind: knowledge.KindNote, Title: "Daily note " + date, Summary: journalSummary(dn.Content)})
			g.AddAlias(id, date)
			dailyByDate[date] = id
			for _, eid := range st.episByDay[date] {
				g.AddLink(knowledge.Link{From: id, To: eid, Rel: knowledge.RelSameDay, Weight: 1})
			}
			facts := st.factsByDate[date]
			if len(facts) > graphJournalMaxFactLinks {
				facts = facts[:graphJournalMaxFactLinks]
			}
			for _, fid := range facts {
				g.AddLink(knowledge.Link{From: id, To: fid, Rel: knowledge.RelSameDay, Weight: 1})
			}
		}
	}
	if m.Rollups == nil {
		return
	}
	var weeklies []memory.Digest
	for _, d := range m.Rollups.Digests() {
		id := "note:" + d.Period + ":" + d.Label
		title := "Week " + d.Label
		if d.Period == "monthly" {
			title = "Month " + d.Label
		}
		g.AddNode(knowledge.Node{ID: id, Kind: knowledge.KindNote, Title: title, Summary: journalSummary(d.Content)})
		g.AddAlias(id, d.Label)
		if d.Period == "weekly" {
			weeklies = append(weeklies, d)
			if start, ok := isoWeekStart(d.Label); ok {
				for i := 0; i < 7; i++ {
					if did, ok := dailyByDate[start.AddDate(0, 0, i).Format("2006-01-02")]; ok {
						g.AddLink(knowledge.Link{From: id, To: did, Rel: knowledge.RelSummarizes, Weight: 1})
					}
				}
			}
			continue
		}
		for _, w := range weeklies {
			if start, ok := isoWeekStart(w.Label); ok && start.Format("2006-01") == d.Label {
				g.AddLink(knowledge.Link{From: id, To: "note:weekly:" + w.Label, Rel: knowledge.RelSummarizes, Weight: 1})
			}
		}
	}
}

// isoWeekStart parses "2026-W27" into the Monday the ISO week starts.
func isoWeekStart(label string) (time.Time, bool) {
	year, week, ok := strings.Cut(label, "-W")
	if !ok {
		return time.Time{}, false
	}
	y, err1 := strconv.Atoi(year)
	w, err2 := strconv.Atoi(week)
	if err1 != nil || err2 != nil || w < 1 || w > 53 {
		return time.Time{}, false
	}
	jan4 := time.Date(y, 1, 4, 0, 0, 0, 0, time.Local)
	monday := jan4.AddDate(0, 0, -((int(jan4.Weekday()) + 6) % 7))
	return monday.AddDate(0, 0, (w-1)*7), true
}

// journalSummary flattens a journal note for its graph node: time headings
// ("## 14:05") dropped, whitespace collapsed, bounded.
func journalSummary(content string) string {
	var parts []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "## ") || strings.HasPrefix(line, "# ") {
			continue
		}
		parts = append(parts, strings.TrimLeft(line, "-* "))
	}
	s := strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
	if r := []rune(s); len(r) > graphJournalSummaryRunes {
		s = strings.TrimSpace(string(r[:graphJournalSummaryRunes])) + "…"
	}
	return s
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
func (src *graphSources) addSessionNodes(g *knowledge.Graph, st *graphBuildState) {
	if src.sessionManager == nil {
		return
	}
	type sess struct {
		name  string
		mtime int64
		day   string
	}
	entries, err := os.ReadDir(src.sessionManager.sessionsDir)
	if err != nil {
		return
	}
	titles := src.sessionManager.warmTitles()
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
		if title != s.name {
			g.AddAlias(id, s.name) // [[session-name]] still resolves
		}
		hay := graphSlug(s.name + " " + title)
		for slug, pid := range st.projByName {
			if slug != "" && strings.Contains(hay, slug) {
				g.AddLink(knowledge.Link{From: id, To: pid, Rel: knowledge.RelInProject, Weight: 1.5})
			}
		}
		for _, eid := range st.episByDay[s.day] {
			g.AddLink(knowledge.Link{From: id, To: eid, Rel: knowledge.RelSameDay, Weight: 1})
		}
	}
}

const (
	// graphMaxDocNodes bounds document nodes across every context, so a
	// handful of large corpora cannot drown the user's own notes; the
	// remaining documents stay reachable through knowledge retrieval.
	graphMaxDocNodes = 6000
	// graphDocLinkWeight is the adjacency weight of a document link — the
	// same weight a [[wikilink]] carries anywhere else.
	graphDocLinkWeight = knowledge.WikilinkWeight
)

// addContextNodes joins /context contexts: knowledge-mode contexts as "kb:"
// nodes (unchanged), every other mode as "ctx:" nodes, each with its tags,
// and — the vault view — one "doc:" node per source document with the links
// the documents write to each other. Edges: kb/ctx↔tag (1.0), doc→context
// part_of (1.0), doc→doc links_to (1.5).
func (src *graphSources) addContextNodes(g *knowledge.Graph, st *graphBuildState) {
	if src.contextHandler == nil {
		return
	}
	mgr := src.contextHandler.GetManager()
	if mgr == nil {
		return
	}
	// Copies taken under the manager lock, oldest first: when the document
	// budget runs out, the contexts the user built first (and has kept)
	// keep their documents.
	ctxs := mgr.SnapshotContexts()
	docBudget := graphMaxDocNodes
	for i := range ctxs {
		fc := &ctxs[i]
		id := contextNodeID(fc)
		kind := knowledge.KindContext
		if fc.Mode == ctxmgr.ModeKnowledge {
			kind = knowledge.KindKB
		}
		g.AddNode(knowledge.Node{ID: id, Kind: kind, Title: fc.Name, Summary: fc.Description})
		for _, tag := range fc.Tags {
			addTag(g, tag)
			g.AddLink(knowledge.Link{From: id, To: tagID(tag), Rel: knowledge.RelTagged, Weight: 1})
		}
		if docBudget <= 0 {
			continue
		}
		docs := mgr.Documents(fc.ID)
		if len(docs) > docBudget {
			docs = docs[:docBudget]
		}
		docBudget -= len(docs)
		for _, d := range docs {
			did := docNodeID(fc.ID, d.Source)
			g.AddNode(knowledge.Node{ID: did, Kind: knowledge.KindDoc, Title: d.Title, Summary: d.Summary})
			g.AddAlias(did, docAlias(d.Source))
			g.AddLink(knowledge.Link{From: did, To: id, Rel: knowledge.RelPartOf, Weight: 1})
		}
		for _, d := range docs {
			did := docNodeID(fc.ID, d.Source)
			for _, target := range d.Links {
				g.AddLink(knowledge.Link{From: did, To: docNodeID(fc.ID, target), Rel: knowledge.RelLinksTo, Origin: knowledge.OriginMarkdown, Weight: graphDocLinkWeight})
			}
			if len(d.Wikilinks) > 0 {
				st.docWikilinks[did] = d.Wikilinks
			}
		}
	}
}

// contextNodeID is the graph id of a context: "kb:" for knowledge mode (the
// id existing graphs and links already use), "ctx:" for every other mode.
func contextNodeID(fc *ctxmgr.FileContext) string {
	if fc.Mode == ctxmgr.ModeKnowledge {
		return "kb:" + fc.ID
	}
	return "ctx:" + fc.ID
}

// docNodeID is the graph id of one document of a context.
func docNodeID(contextID, source string) string {
	return "doc:" + contextID + ":" + source
}

// docAlias is the name a [[wikilink]] most naturally uses for a document:
// its file name without extension.
func docAlias(source string) string {
	base := filepath.Base(strings.ReplaceAll(source, "\\", "/"))
	if i := strings.LastIndexByte(base, '.'); i > 0 {
		base = base[:i]
	}
	return base
}

// linkDocWikilinks resolves the [[targets]] documents wrote that named
// nothing in their own context against the whole vault (a doc may link to
// a project, a topic or a document of another context).
func linkDocWikilinks(g *knowledge.Graph, st *graphBuildState) {
	ids := make([]string, 0, len(st.docWikilinks))
	for id := range st.docWikilinks {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		for _, target := range st.docWikilinks[id] {
			if t, ok := g.Resolve(target); ok {
				g.AddLink(knowledge.Link{From: id, To: t.ID, Rel: knowledge.RelLinksTo, Origin: knowledge.OriginWikilink, Weight: knowledge.WikilinkWeight})
			}
		}
	}
}

// graphUserLinkWeight is the adjacency weight of an explicit link: as strong
// as the strongest structural relationship (topic↔fact, fact→project).
const graphUserLinkWeight = 2

// addStoredLinks replays the explicit links of links.json. A link drawn to a
// fact that was later superseded follows the supersession chain to the
// current fact; a link whose endpoint does not exist (yet) is skipped here
// and kept in the store (LinkStore.GC owns its lifetime).
func (src *graphSources) addStoredLinks(g *knowledge.Graph, st *graphBuildState) {
	if src.memoryStore == nil {
		return
	}
	links := src.memoryStore.Manager().Links
	if links == nil {
		return
	}
	for _, l := range links.All() {
		g.AddLink(knowledge.Link{
			From:   resolveSuperseded(l.From, st.supersededBy),
			To:     resolveSuperseded(l.To, st.supersededBy),
			Rel:    knowledge.Rel(l.Rel),
			Origin: knowledge.Origin(l.Origin),
			Weight: graphUserLinkWeight,
		})
	}
}

// resolveSuperseded follows "fact:<old>" through the supersession chain
// (bounded, cycle-safe) to the fact that currently holds its place.
func resolveSuperseded(id string, by map[string]string) string {
	if !strings.HasPrefix(id, "fact:") || len(by) == 0 {
		return id
	}
	cur := strings.TrimPrefix(id, "fact:")
	for i := 0; i < 16; i++ {
		next, ok := by[cur]
		if !ok || next == cur {
			break
		}
		cur = next
	}
	return "fact:" + cur
}

func (src *graphSources) addSkillNodes(g *knowledge.Graph) {
	if src.personaHandler == nil {
		return
	}
	skills, err := src.personaHandler.GetManager().ListSkills()
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
			g.AddLink(knowledge.Link{From: id, To: "topic:" + slug, Rel: knowledge.RelTriggeredBy, Weight: 1})
			addTag(g, tr)
			g.AddLink(knowledge.Link{From: id, To: tagID(tr), Rel: knowledge.RelTriggeredBy, Weight: 0.5})
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
		// A title, alias or [[wikilink]] names its note exactly; free text
		// falls back to the best keyword match, as before.
		if n, found := g.Resolve(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(idOrQuery), "[["), "]]")); found {
			seed = n
		} else if hits := g.Search(strings.Fields(idOrQuery), 1); len(hits) > 0 {
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
	b.WriteString("Connected (→ links it writes, ← backlinks):\n")
	for _, n := range hood {
		writeGraphNode(&b, n, relationLabel(g, seed.ID, n.ID))
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
func (src *graphSources) graphFingerprint() string {
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
	if src.memoryStore != nil {
		md := src.memoryStore.Manager().MemoryDir()
		for _, f := range []string{
			"memory_index.json", "topics.json", "projects.json",
			"user_profile.json", "episodes.json", "links.json",
		} {
			statFile(filepath.Join(md, f))
		}
		// Journal: daily notes live in YYYYMM directories, digests in
		// weekly/ and monthly/.
		if entries, err := os.ReadDir(md); err == nil {
			for _, e := range entries {
				if e.IsDir() && len(e.Name()) == 6 && strings.Trim(e.Name(), "0123456789") == "" {
					statDir(filepath.Join(md, e.Name()))
				}
			}
		}
		statDir(filepath.Join(md, "weekly"))
		statDir(filepath.Join(md, "monthly"))
	}
	if src.personaHandler != nil {
		if mgr := src.personaHandler.GetManager(); mgr != nil {
			for _, d := range mgr.SkillDirs() {
				statDir(d)
			}
		}
	}
	if src.sessionManager != nil {
		statDir(src.sessionManager.sessionsDir)
	}
	if src.contextHandler != nil {
		if mgr := src.contextHandler.GetManager(); mgr != nil && mgr.Storage != nil {
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
	if cli.memoryStore == nil {
		return
	}
	src := cli.graphSources()
	// Link GC judges endpoints by the stores themselves, whether or not
	// the persisted graph is on.
	cli.memoryStore.Manager().SetLinkExistence(src.linkExistence)
	if !memoryGraphEnabled() {
		return
	}
	cli.memoryStore.Manager().SetGraphSource(src.build, src.graphFingerprint)
}

// linkExistence returns the endpoint check one link-GC pass uses: does
// this node id still name something in the bound STORES? Deliberately not
// the graph, which caps sessions (50), episodes (300) and documents. Facts
// follow the supersession chain (a link to a replaced fact lives on in its
// successor). Tags, and any id shape it does not know, always exist — GC
// must only ever drop what it is sure is gone.
func (src *graphSources) linkExistence() func(id string) bool {
	if src.memoryStore == nil {
		return nil
	}
	idx := src.newLinkIndex()
	return idx.exists
}

// linkIndex is one GC pass's view of the stores.
type linkIndex struct {
	facts       map[string]bool
	superseded  map[string]string
	ids         map[string]bool // topic:, project:, episode:, note:, skill: ids
	skillsKnown bool
	hasProfile  bool
	ctxMgr      *ctxmgr.Manager
	sessionsDir string
}

func (src *graphSources) newLinkIndex() *linkIndex {
	m := src.memoryStore.Manager()
	idx := &linkIndex{
		facts:      make(map[string]bool),
		superseded: make(map[string]string),
		ids:        make(map[string]bool),
		hasProfile: !m.Profile.IsEmpty(),
	}
	for _, f := range m.Facts.GetAll() {
		idx.facts[f.ID] = true
		if old := supersededFactID(f.Provenance); old != "" && old != f.ID {
			idx.superseded[old] = f.ID
		}
	}
	for _, tp := range m.Topics.GetAll() {
		idx.ids["topic:"+graphSlug(tp.Name)] = true
	}
	for _, p := range m.Projects.GetAll() {
		idx.ids["project:"+graphSlug(p.Name)] = true
	}
	for _, e := range m.Episodes.Range(zeroTime, zeroTime, "", "", 0) {
		idx.ids["episode:"+e.ID] = true
	}
	if m.Daily != nil {
		for _, dn := range m.Daily.GetRecentDailyNotes(m.GetConfig().DailyNoteRetention) {
			idx.ids["note:daily:"+dn.Date.Format("2006-01-02")] = true
		}
	}
	if m.Rollups != nil {
		for _, d := range m.Rollups.Digests() {
			idx.ids["note:"+d.Period+":"+d.Label] = true
		}
	}
	if src.personaHandler != nil {
		if list, err := src.personaHandler.GetManager().ListSkills(); err == nil {
			idx.skillsKnown = true
			for _, sk := range list {
				if sk != nil {
					idx.ids["skill:"+sk.Name] = true
				}
			}
		}
	}
	if src.contextHandler != nil {
		idx.ctxMgr = src.contextHandler.GetManager()
	}
	if src.sessionManager != nil {
		idx.sessionsDir = src.sessionManager.sessionsDir
	}
	return idx
}

func (idx *linkIndex) exists(id string) bool {
	kind, rest, ok := strings.Cut(id, ":")
	if !ok {
		return true
	}
	switch kind {
	case "fact":
		return idx.facts[strings.TrimPrefix(resolveSuperseded(id, idx.superseded), "fact:")]
	case "topic", "project", "episode", "note":
		return idx.ids[id]
	case "skill":
		return !idx.skillsKnown || idx.ids[id]
	case "profile":
		return idx.hasProfile
	case "session":
		return idx.fileExists(rest)
	case "card":
		_, err := boardStore().Get(rest)
		return !errors.Is(err, board.ErrNotFound) // an unreadable board proves nothing
	case "kb", "ctx", "doc":
		return idx.contextExists(kind, rest)
	}
	return true // tags and unknown shapes: never collected
}

func (idx *linkIndex) fileExists(session string) bool {
	if idx.sessionsDir == "" {
		return true
	}
	_, err := os.Stat(filepath.Join(idx.sessionsDir, session+".json"))
	return err == nil
}

func (idx *linkIndex) contextExists(kind, rest string) bool {
	if idx.ctxMgr == nil {
		return true
	}
	ctxID, source, isDoc := rest, "", false
	if kind == "doc" {
		var ok bool
		if ctxID, source, ok = strings.Cut(rest, ":"); !ok {
			return true
		}
		isDoc = true
	}
	if _, err := idx.ctxMgr.GetContext(ctxID); err != nil {
		// Not loaded is not deleted: a context file that failed to load
		// (decryption, permissions, quarantine) is still the user's.
		return idx.ctxMgr.Storage != nil && idx.ctxMgr.Storage.ContextFileExists(ctxID)
	}
	return !isDoc || idx.ctxMgr.HasDocument(ctxID, source)
}

// writeGraphNode renders one neighbor line; rel ("→ about", "← mentions")
// is appended when the node links directly with the seed.
func writeGraphNode(b *strings.Builder, n *knowledge.Node, rel string) {
	title := strings.TrimSpace(n.Title)
	if title == "" {
		title = n.ID
	}
	suffix := ""
	if rel != "" {
		suffix = "  (" + rel + ")"
	}
	if summary := strings.TrimSpace(n.Summary); summary != "" && summary != title {
		fmt.Fprintf(b, "  - [%s] %s — %s%s\n", n.Kind, title, truncateForLog(summary, 160), suffix)
	} else {
		fmt.Fprintf(b, "  - [%s] %s%s\n", n.Kind, title, suffix)
	}
}

// relationLabel annotates a neighbor of seed with how they are linked:
// "→ rel" for links seed writes, "← rel" for backlinks. Empty for nodes
// reached through another note (2 hops).
func relationLabel(g *knowledge.Graph, seed, other string) string {
	var parts []string
	seen := map[string]bool{}
	for _, l := range g.LinksBetween(seed, other) {
		arrow := "→ "
		if l.From == other {
			arrow = "← "
		}
		label := arrow + string(l.Rel)
		if !seen[label] {
			seen[label] = true
			parts = append(parts, label)
		}
	}
	return strings.Join(parts, ", ")
}
