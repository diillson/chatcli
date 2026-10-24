/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * links.go — typed, directed links: the Obsidian half of the graph.
 *
 * The adjacency map (graph.go) is an undirected weighted index that ranking
 * and neighborhood expansion read. A Link is the note-level fact behind an
 * edge: WHICH note points at which (direction, so backlinks exist), WHY
 * (Rel: mentions, about, links_to, part_of, …) and HOW the link was learned
 * (Origin: a [[wikilink]], a markdown link, an autolinked mention, a user
 * command, or a relationship a store already records).
 *
 * AddLink always feeds the adjacency with exactly the weight AddEdge would
 * have received, so a graph built through links ranks byte-for-byte like
 * the legacy untyped one; the link table is purely additive metadata.
 */
package knowledge

import (
	"sort"
	"strings"
	"unicode"

	"github.com/diillson/chatcli/pkg/textfold"
)

// Rel names why one note links to another. Stable string constants: they
// are persisted (graph.json, links.json) and rendered to the model.
type Rel string

const (
	// RelRelated is the untyped default (legacy AddEdge, user links with no
	// relation given).
	RelRelated Rel = "related"
	// RelTagged links a note to a tag it carries.
	RelTagged Rel = "tagged"
	// RelCategory links a fact to its category tag.
	RelCategory Rel = "category"
	// RelInProject links a fact, episode or session to its project.
	RelInProject Rel = "in_project"
	// RelAbout links a topic to a fact recorded under it.
	RelAbout Rel = "about"
	// RelUses links a project to a technology it uses.
	RelUses Rel = "uses"
	// RelHas links the user to a declared skill or goal.
	RelHas Rel = "has"
	// RelSameDay links timeline entries (episode, session, fact) that
	// happened on the same day in the same project.
	RelSameDay Rel = "same_day"
	// RelMentions is an autolinked mention: the source note's text names the
	// target note (Obsidian "unlinked mention", resolved).
	RelMentions Rel = "mentions"
	// RelLinksTo is an explicit link written in the source text — a
	// [[wikilink]] or a relative markdown link.
	RelLinksTo Rel = "links_to"
	// RelPartOf links a document to the context (knowledge base) holding it.
	RelPartOf Rel = "part_of"
	// RelSupersedes links a fact to the older fact it replaced.
	RelSupersedes Rel = "supersedes"
	// RelAssigned links a board card to its assignee.
	RelAssigned Rel = "assigned"
	// RelTriggeredBy links a skill to a trigger keyword.
	RelTriggeredBy Rel = "triggered_by"
	// RelSummarizes links a rollup digest to the notes of the period it
	// condenses (weekly → daily notes, monthly → weeklies).
	RelSummarizes Rel = "summarizes"
)

// Origin records how a link was learned, so consumers can weigh an explicit
// user link above an autolinked mention.
type Origin string

const (
	// OriginStructural is a relationship a source store already records
	// (topic↔fact, fact→project, shared tags, same day).
	OriginStructural Origin = "structural"
	// OriginWikilink is a [[wikilink]] parsed from note text.
	OriginWikilink Origin = "wikilink"
	// OriginMarkdown is a relative markdown link between documents.
	OriginMarkdown Origin = "markdown"
	// OriginMention is an autolinked mention of another note's title.
	OriginMention Origin = "mention"
	// OriginUser is a link the user (or the model, on the user's behalf)
	// created explicitly with @memory link or /memory link.
	OriginUser Origin = "user"
)

// Link is one directed, typed link between two nodes.
type Link struct {
	From   string  `json:"from"`
	To     string  `json:"to"`
	Rel    Rel     `json:"rel"`
	Origin Origin  `json:"origin,omitempty"`
	Weight float64 `json:"w,omitempty"`
}

// linkKey identifies a link: one record per (from, to, rel).
type linkKey struct {
	from, to string
	rel      Rel
}

// AddLink records a directed typed link and feeds the undirected adjacency
// with its weight, exactly like AddEdge. It is a no-op (returning false)
// when either endpoint is missing or From == To. Re-adding the same
// (From, To, Rel) accumulates weight on the record — again matching what
// AddEdge does to the adjacency — and keeps the first Origin unless the new
// one is explicit user intent.
func (g *Graph) AddLink(l Link) bool {
	l = normalizeLink(l)
	if l.From == l.To {
		return false
	}
	if _, ok := g.nodes[l.From]; !ok {
		return false
	}
	if _, ok := g.nodes[l.To]; !ok {
		return false
	}
	g.AddEdge(l.From, l.To, l.Weight)
	g.recordLink(l)
	return true
}

// recordLink stores link metadata without touching the adjacency — the
// deserialization path, where edge weights arrive already accumulated.
func (g *Graph) recordLink(l Link) {
	if g.linkTab == nil {
		g.linkTab = make(map[linkKey]*Link)
		g.outIdx = make(map[string][]linkKey)
		g.inIdx = make(map[string][]linkKey)
	}
	k := linkKey{from: l.From, to: l.To, rel: l.Rel}
	if cur, ok := g.linkTab[k]; ok {
		cur.Weight += l.Weight
		if l.Origin == OriginUser {
			cur.Origin = OriginUser
		}
		return
	}
	cp := l
	g.linkTab[k] = &cp
	g.outIdx[l.From] = append(g.outIdx[l.From], k)
	g.inIdx[l.To] = append(g.inIdx[l.To], k)
}

func normalizeLink(l Link) Link {
	l.From = strings.TrimSpace(l.From)
	l.To = strings.TrimSpace(l.To)
	if l.Rel == "" {
		l.Rel = RelRelated
	}
	if l.Origin == "" {
		l.Origin = OriginStructural
	}
	if l.Weight <= 0 {
		l.Weight = 1
	}
	return l
}

// LinkCount is the number of distinct typed links.
func (g *Graph) LinkCount() int { return len(g.linkTab) }

// Outlinks returns the links leaving id (what this note points at), ordered
// by weight desc, then rel, then target id.
func (g *Graph) Outlinks(id string) []Link {
	return g.collect(g.outIdx[id])
}

// Backlinks returns the links arriving at id (what points at this note),
// ordered by weight desc, then rel, then source id.
func (g *Graph) Backlinks(id string) []Link {
	return g.collect(g.inIdx[id])
}

// LinksBetween returns every typed link between a and b in either
// direction, ordered like Outlinks.
func (g *Graph) LinksBetween(a, b string) []Link {
	var keys []linkKey
	for _, k := range g.outIdx[a] {
		if k.to == b {
			keys = append(keys, k)
		}
	}
	for _, k := range g.outIdx[b] {
		if k.to == a {
			keys = append(keys, k)
		}
	}
	return g.collect(keys)
}

// Links returns every typed link, sorted by (From, To, Rel).
func (g *Graph) Links() []Link {
	out := make([]Link, 0, len(g.linkTab))
	for _, l := range g.linkTab {
		out = append(out, *l)
	}
	sort.Slice(out, func(i, j int) bool { return linkLess(out[i], out[j]) })
	return out
}

func (g *Graph) collect(keys []linkKey) []Link {
	out := make([]Link, 0, len(keys))
	for _, k := range keys {
		if l, ok := g.linkTab[k]; ok {
			out = append(out, *l)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Weight != out[j].Weight {
			return out[i].Weight > out[j].Weight
		}
		return linkLess(out[i], out[j])
	})
	return out
}

func linkLess(a, b Link) bool {
	if a.From != b.From {
		return a.From < b.From
	}
	if a.To != b.To {
		return a.To < b.To
	}
	return a.Rel < b.Rel
}

// --- aliases and reference resolution ([[wikilink]] targets) ---

// AddAlias registers an alternative name a reference may use for node id
// (Obsidian frontmatter aliases). Blank aliases and unknown ids are ignored.
func (g *Graph) AddAlias(id, alias string) {
	if _, ok := g.nodes[id]; !ok {
		return
	}
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return
	}
	if g.aliases == nil {
		g.aliases = make(map[string][]string)
	}
	for _, a := range g.aliases[id] {
		if a == alias {
			return
		}
	}
	g.aliases[id] = append(g.aliases[id], alias)
	g.refIdx = nil // rebuilt lazily on the next Resolve
}

// Aliases returns the aliases registered for id, sorted.
func (g *Graph) Aliases(id string) []string {
	out := append([]string(nil), g.aliases[id]...)
	sort.Strings(out)
	return out
}

// resolvePriority orders kinds when several notes share a name: the more
// specific, user-curated notes win over generic glue (a project named
// "auth" beats a tag "auth").
var resolvePriority = map[Kind]int{
	KindProject: 0, KindTopic: 1, KindKB: 2, KindContext: 2, KindSkill: 3,
	KindDoc: 4, KindProfile: 5, KindNote: 6, KindSession: 6, KindCard: 7,
	KindEpisode: 8, KindFact: 9, KindTag: 10,
}

func priorityOf(k Kind) int {
	if p, ok := resolvePriority[k]; ok {
		return p
	}
	return 50
}

// NormalizeRef folds a reference for name matching: accents removed, case
// folded, separators (space, -, _) collapsed to single spaces. Exposed so
// every surface (wikilinks, @memory link, vault) matches names one way.
func NormalizeRef(s string) string {
	s = strings.ToLower(textfold.Fold(strings.TrimSpace(s)))
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) || r == '-' || r == '_' {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}

// Resolve maps a reference — an exact node id, a title or an alias,
// matched through NormalizeRef — to a node. Ties between notes sharing a
// name break by kind priority, then node id, so resolution is
// deterministic. A [[target|label]] or [[target#heading]] reference should
// be passed as its target part only.
func (g *Graph) Resolve(ref string) (*Node, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, false
	}
	if n, ok := g.nodes[ref]; ok {
		return n, true
	}
	ids := g.refIndex()[NormalizeRef(ref)]
	if len(ids) == 0 {
		return nil, false
	}
	return g.nodes[ids[0]], true
}

// ResolveAll returns every node a reference names, best first (same order
// as Resolve), so callers can surface ambiguity instead of guessing.
func (g *Graph) ResolveAll(ref string) []*Node {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil
	}
	if n, ok := g.nodes[ref]; ok {
		return []*Node{n}
	}
	ids := g.refIndex()[NormalizeRef(ref)]
	out := make([]*Node, 0, len(ids))
	for _, id := range ids {
		out = append(out, g.nodes[id])
	}
	return out
}

// refIndex lazily builds normalized-name → node ids (best first). Graphs
// are built single-threaded and then served as read-only snapshots; the
// cache builds the index once before publishing (see Freeze), so concurrent
// readers never race on it.
func (g *Graph) refIndex() map[string][]string {
	if g.refIdx != nil {
		return g.refIdx
	}
	idx := make(map[string][]string, len(g.nodes))
	add := func(name, id string) {
		key := NormalizeRef(name)
		if key == "" {
			return
		}
		for _, existing := range idx[key] {
			if existing == id {
				return
			}
		}
		idx[key] = append(idx[key], id)
	}
	for id, n := range g.nodes {
		add(n.Title, id)
	}
	for id, as := range g.aliases {
		for _, a := range as {
			add(a, id)
		}
	}
	for key, ids := range idx {
		sort.Slice(ids, func(i, j int) bool {
			pi, pj := priorityOf(g.nodes[ids[i]].Kind), priorityOf(g.nodes[ids[j]].Kind)
			if pi != pj {
				return pi < pj
			}
			return ids[i] < ids[j]
		})
		idx[key] = ids
	}
	g.refIdx = idx
	return idx
}

// Freeze precomputes the lazily-built indexes so the graph can be shared
// read-only across goroutines. Call once after building, before publishing.
func (g *Graph) Freeze() {
	g.refIndex()
}

// ParseWikilinks extracts the targets of every [[target]], [[target|label]]
// and [[target#heading]] in text, in order of appearance, deduplicated.
// Embeds (![[x]]) count as links too, as in Obsidian.
func ParseWikilinks(text string) []string {
	var out []string
	seen := make(map[string]bool)
	for {
		start := strings.Index(text, "[[")
		if start < 0 {
			break
		}
		rest := text[start+2:]
		end := strings.Index(rest, "]]")
		if end < 0 {
			break
		}
		inner := rest[:end]
		text = rest[end+2:]
		if strings.ContainsAny(inner, "\n[") {
			continue
		}
		if i := strings.IndexByte(inner, '|'); i >= 0 {
			inner = inner[:i]
		}
		if i := strings.IndexByte(inner, '#'); i >= 0 {
			inner = inner[:i]
		}
		if i := strings.IndexByte(inner, '^'); i >= 0 {
			inner = inner[:i]
		}
		inner = strings.TrimSpace(inner)
		if inner == "" || seen[inner] {
			continue
		}
		seen[inner] = true
		out = append(out, inner)
	}
	return out
}
