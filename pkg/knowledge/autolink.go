/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * autolink.go — the two text-driven link passes of the vault model:
 *
 *   - LinkWikilinks: every [[target]] written in a note's text becomes a
 *     links_to link to the note that target resolves to;
 *   - LinkMentions: a note whose text names another note's title or alias
 *     (word-boundary, accent/case-insensitive) gets a mentions link to it —
 *     Obsidian's "unlinked mentions", resolved automatically, so memory
 *     written in plain prose still forms a connected graph.
 *
 * Both are deterministic for a given graph (sorted iteration everywhere), so
 * the persisted graph stays byte-stable between unchanged rebuilds.
 */
package knowledge

import (
	"sort"
	"strings"
	"unicode"
)

// WikilinkWeight is the adjacency weight of a [[wikilink]] — unchanged from
// the untyped graph, so explicit links rank exactly as they always did.
const WikilinkWeight = 1.5

// LinkWikilinks resolves the [[wikilinks]] in every node's summary and adds
// a links_to link per resolved target. Unresolvable targets are skipped (a
// note can link to something that does not exist yet). Document nodes are
// skipped: their links come from the full document text (the context
// document pass), and counting their summary too would double the weight.
//
// Compatibility: the untyped graph resolved a target to the first node, in
// id order, whose lowercased title matched exactly. When that legacy target
// differs from the one Resolve picks (a tag "auth" vs a topic "auth"), the
// legacy edge is kept as well, so no relationship a user had disappears.
// Returns the number of links added.
func (g *Graph) LinkWikilinks() int {
	legacy := make(map[string]string)
	for _, n := range g.Nodes() { // id order, as the untyped graph iterated
		if n.Kind == KindDoc || n.Kind == KindContext || n.Kind == KindNote {
			continue // kinds the untyped graph never had cannot be its target
		}
		if t := strings.ToLower(strings.TrimSpace(n.Title)); t != "" {
			if _, taken := legacy[t]; !taken {
				legacy[t] = n.ID
			}
		}
	}
	added := 0
	link := func(from, to string) {
		if g.AddLink(Link{From: from, To: to, Rel: RelLinksTo, Origin: OriginWikilink, Weight: WikilinkWeight}) {
			added++
		}
	}
	for _, n := range g.Nodes() {
		if n.Kind == KindDoc {
			continue
		}
		for _, target := range ParseWikilinks(n.Summary) {
			resolved := ""
			if t, ok := g.Resolve(target); ok {
				resolved = t.ID
				link(n.ID, resolved)
			}
			if lid, ok := legacy[strings.ToLower(strings.TrimSpace(target))]; ok && lid != resolved {
				link(n.ID, lid)
			}
		}
	}
	return added
}

// MentionOptions configures LinkMentions.
type MentionOptions struct {
	// SourceKinds are the kinds whose text is scanned for mentions.
	SourceKinds []Kind
	// TargetKinds are the kinds whose titles and aliases can be mentioned.
	TargetKinds []Kind
	// MinRunes is the shortest name that may be autolinked (shorter names
	// match too much prose). Values below 3 are raised to 3.
	MinRunes int
	// MaxPerSource caps mentions linked from one note (0 = 8).
	MaxPerSource int
	// Weight is the adjacency weight of a mention link (0 = 1).
	Weight float64
}

// mentionStopwords are single-word names too generic to autolink: they
// occur in ordinary prose far more often than as references to a note.
var mentionStopwords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "this": true, "that": true,
	"user": true, "uses": true, "use": true, "using": true, "new": true, "old": true,
	"general": true, "project": true, "projects": true, "topic": true, "note": true,
	"notes": true, "file": true, "files": true, "code": true, "data": true, "test": true,
	"tests": true, "main": true, "work": true, "todo": true, "done": true, "doing": true,
	"para": true, "com": true, "uma": true, "por": true, "que": true, "dos": true,
	"das": true, "nao": true, "sim": true, "mais": true, "como": true, "isso": true,
	"usuario": true, "projeto": true, "arquivo": true, "codigo": true, "dados": true,
}

// mentionName is one autolinkable name split into tokens.
type mentionName struct {
	tokens []string
	id     string
	prio   int
}

// LinkMentions adds a mentions link from every source note to every target
// note whose title or alias appears in the source's text as a whole token
// sequence. Pairs that are already linked in either direction are skipped —
// the mention would only restate a relationship the graph has. When several
// notes share a name, the one Resolve would pick wins. Returns the number of
// links added.
func (g *Graph) LinkMentions(opt MentionOptions) int {
	if opt.MinRunes < 3 {
		opt.MinRunes = 3
	}
	if opt.MaxPerSource <= 0 {
		opt.MaxPerSource = 8
	}
	if opt.Weight <= 0 {
		opt.Weight = 1
	}
	targets := kindSet(opt.TargetKinds)
	sources := kindSet(opt.SourceKinds)

	// Dictionary keyed by first token; longest names first so "kubernetes
	// operator" wins over "kubernetes" at the same position.
	dict := make(map[string][]mentionName)
	owner := make(map[string]mentionName) // normalized name → best node
	for _, n := range g.Nodes() {
		if !targets[n.Kind] {
			continue
		}
		// Titles only: aliases (a project's directory name, a session's
		// file name) are generic words in prose ("server", "docs") and
		// stay reachable through [[wikilinks]] and Resolve.
		for _, name := range []string{n.Title} {
			toks := mentionTokens(name)
			if !autolinkable(toks, opt.MinRunes) {
				continue
			}
			key := strings.Join(toks, " ")
			cand := mentionName{tokens: toks, id: n.ID, prio: priorityOf(n.Kind)}
			if cur, ok := owner[key]; ok && (cur.prio < cand.prio || (cur.prio == cand.prio && cur.id < cand.id)) {
				continue
			}
			owner[key] = cand
		}
	}
	keys := make([]string, 0, len(owner))
	for k := range owner {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		mn := owner[k]
		dict[mn.tokens[0]] = append(dict[mn.tokens[0]], mn)
	}
	for first, list := range dict {
		sort.SliceStable(list, func(i, j int) bool { return len(list[i].tokens) > len(list[j].tokens) })
		dict[first] = list
	}
	if len(dict) == 0 {
		return 0
	}

	added := 0
	for _, n := range g.Nodes() {
		if !sources[n.Kind] {
			continue
		}
		// Title and summary both carry prose (a card's title names its
		// subject; a fact's title is a prefix of its summary, and repeated
		// hits on one target are deduplicated below).
		toks := mentionTokens(n.Title + "\n" + n.Summary)
		linked := 0
		hit := make(map[string]bool)
		for i := 0; i < len(toks) && linked < opt.MaxPerSource; i++ {
			for _, mn := range dict[toks[i]] {
				if !tokensAt(toks, i, mn.tokens) {
					continue
				}
				// The longest name at this position owns its tokens: a
				// shorter name inside it ("operator" in "kubernetes
				// operator") is not a separate mention.
				skip := len(mn.tokens) - 1
				if mn.id == n.ID || hit[mn.id] {
					i += skip
					break
				}
				hit[mn.id] = true
				if len(g.LinksBetween(n.ID, mn.id)) > 0 || g.adj[n.ID][mn.id] > 0 {
					i += skip
					break // already related: the mention adds nothing
				}
				if g.AddLink(Link{From: n.ID, To: mn.id, Rel: RelMentions, Origin: OriginMention, Weight: opt.Weight}) {
					added++
					linked++
				}
				i += skip
				break
			}
		}
	}
	return added
}

func kindSet(kinds []Kind) map[Kind]bool {
	out := make(map[Kind]bool, len(kinds))
	for _, k := range kinds {
		out[k] = true
	}
	return out
}

// mentionTokens splits normalized text into word tokens. '+' and '#' stay
// inside tokens so names like "C++" and "C#" survive; every other
// non-alphanumeric rune separates.
func mentionTokens(s string) []string {
	s = NormalizeRef(s)
	return strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '+' && r != '#'
	})
}

// autolinkable rejects names that would match ordinary prose: too short,
// purely numeric, or a single generic word.
func autolinkable(toks []string, minRunes int) bool {
	if len(toks) == 0 {
		return false
	}
	total := 0
	digits := true
	for _, t := range toks {
		total += len([]rune(t))
		for _, r := range t {
			if !unicode.IsDigit(r) {
				digits = false
			}
		}
	}
	if total < minRunes || digits {
		return false
	}
	if len(toks) == 1 && (mentionStopwords[toks[0]] || len([]rune(toks[0])) < 4) {
		// One short word ("go", "api") is ambiguous in prose; it still
		// links through tags, wikilinks and explicit links.
		return false
	}
	return true
}

func tokensAt(toks []string, i int, want []string) bool {
	if i+len(want) > len(toks) {
		return false
	}
	for j, w := range want {
		if toks[i+j] != w {
			return false
		}
	}
	return true
}
