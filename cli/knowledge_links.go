/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * knowledge_links.go — explicit links between notes (@memory link/unlink,
 * /memory link/unlink) and the reference resolution they share.
 *
 * A reference is whatever a person or the model naturally writes for a note:
 * an exact node id ("topic:auth"), a fact id or id prefix as @memory list
 * shows it, a title or alias ("Orbital", "[[Setup]]"), or free text that
 * names exactly one note. Anything that could mean more than one note is
 * refused with the candidates and their ids — a link drawn to the wrong note
 * is wrong user data, so it is never guessed.
 */
package cli

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/diillson/chatcli/cli/workspace/memory"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/pkg/knowledge"
)

// factIDPrefixRe matches a bare fact id or id prefix as @memory list prints
// it (hex, at least 6 characters).
var factIDPrefixRe = regexp.MustCompile(`^[0-9a-f]{6,}$`)

// maxRefCandidates bounds the candidates listed for an ambiguous reference.
const maxRefCandidates = 5

// errRefNotFound and errRefAmbiguous classify resolution failures.
var (
	errRefNotFound  = errors.New("no note matches")
	errRefAmbiguous = errors.New("reference is ambiguous")
)

// resolveNoteRef maps a reference to exactly one node of g.
func resolveNoteRef(g *knowledge.Graph, ref string) (*knowledge.Node, []*knowledge.Node, error) {
	ref = strings.TrimSpace(ref)
	ref = strings.TrimSuffix(strings.TrimPrefix(ref, "[["), "]]")
	if t := knowledge.ParseWikilinks("[[" + ref + "]]"); len(t) == 1 {
		ref = t[0] // [[target|label]] / [[target#heading]] → target
	}
	if ref == "" {
		return nil, nil, errRefNotFound
	}
	if n, ok := g.Node(ref); ok {
		return n, nil, nil
	}
	if factIDPrefixRe.MatchString(strings.ToLower(ref)) {
		var hits []*knowledge.Node
		for _, n := range g.Nodes() {
			if n.Kind == knowledge.KindFact && strings.HasPrefix(n.ID, "fact:"+strings.ToLower(ref)) {
				hits = append(hits, n)
			}
		}
		switch len(hits) {
		case 1:
			return hits[0], nil, nil
		case 0:
		default:
			return nil, capNodes(hits), errRefAmbiguous
		}
	}
	if all := g.ResolveAll(ref); len(all) > 0 {
		// A name shared by notes of different kinds resolves by kind
		// priority (a project beats a tag); a tie within the top kind is
		// genuinely ambiguous.
		if len(all) == 1 || all[1].Kind != all[0].Kind {
			return all[0], nil, nil
		}
		return nil, capNodes(all), errRefAmbiguous
	}
	// Free text: only a note containing EVERY word of the reference, and
	// only when exactly one does — a partial keyword hit is a guess.
	words := strings.Fields(ref)
	var full []*knowledge.Node
	for _, n := range g.Search(words, 64) {
		if containsAllWords(n.Title+" "+n.Summary, words) {
			full = append(full, n)
		}
	}
	switch len(full) {
	case 0:
		return nil, nil, errRefNotFound
	case 1:
		return full[0], nil, nil
	}
	return nil, capNodes(full), errRefAmbiguous
}

// containsAllWords reports whether text holds every word (case-folded).
func containsAllWords(text string, words []string) bool {
	hay := strings.ToLower(text)
	for _, w := range words {
		if !strings.Contains(hay, strings.ToLower(w)) {
			return false
		}
	}
	return true
}

func capNodes(ns []*knowledge.Node) []*knowledge.Node {
	if len(ns) > maxRefCandidates {
		return ns[:maxRefCandidates]
	}
	return ns
}

// refError renders a resolution failure for the model/user, listing the
// candidates with ids so the next call can be exact.
func refError(ref string, cands []*knowledge.Node, err error) error {
	if errors.Is(err, errRefAmbiguous) {
		var b strings.Builder
		for _, n := range cands {
			fmt.Fprintf(&b, "\n  - %s  [%s] %s", n.ID, n.Kind, strings.TrimSpace(n.Title))
		}
		return errors.New(i18n.T("graph.link.ambiguous", ref, b.String()))
	}
	return errors.New(i18n.T("graph.link.not_found", ref))
}

// graphLinkText draws an explicit link from → to (persisted in links.json).
func (cli *ChatCLI) graphLinkText(from, to, rel, note string) (string, error) {
	if cli.memoryStore == nil || cli.memoryStore.Manager().Links == nil {
		return "", errors.New(i18n.T("graph.link.unavailable"))
	}
	g := cli.knowledgeGraph()
	a, ac, err := resolveNoteRef(g, from)
	if err != nil {
		return "", refError(from, ac, err)
	}
	b, bc, err := resolveNoteRef(g, to)
	if err != nil {
		return "", refError(to, bc, err)
	}
	if a.ID == b.ID {
		return "", errors.New(i18n.T("graph.link.self", a.ID))
	}
	links := cli.memoryStore.Manager().Links
	storedRel := normalizeLinkRel(rel)
	changed, err := links.Add(a.ID, b.ID, storedRel, string(knowledge.OriginUser), note)
	switch {
	case errors.Is(err, memory.ErrLinksFull):
		return "", errors.New(i18n.T("graph.link.full"))
	case errors.Is(err, memory.ErrLinksReadOnly):
		return "", errors.New(i18n.T("graph.link.readonly"))
	case err != nil:
		return "", errors.New(i18n.T("graph.link.invalid", a.ID, b.ID))
	case !changed:
		return i18n.T("graph.link.exists", describeNode(a), describeNode(b), storedRel), nil
	}
	cli.invalidateMemoryPrompt()
	return i18n.T("graph.link.created", describeNode(a), describeNode(b), storedRel), nil
}

// graphUnlinkText removes the explicit links between two notes (both
// directions; one relation when rel is set). Derived links cannot be
// removed — they restate the data and would be re-derived — so the reply
// says where they come from instead.
func (cli *ChatCLI) graphUnlinkText(from, to, rel string) (string, error) {
	if cli.memoryStore == nil || cli.memoryStore.Manager().Links == nil {
		return "", errors.New(i18n.T("graph.link.unavailable"))
	}
	g := cli.knowledgeGraph()
	a, ac, err := resolveNoteRef(g, from)
	if err != nil {
		return "", refError(from, ac, err)
	}
	b, bc, err := resolveNoteRef(g, to)
	if err != nil {
		return "", refError(to, bc, err)
	}
	if strings.TrimSpace(rel) != "" {
		rel = normalizeLinkRel(rel)
	}
	n := cli.memoryStore.Manager().Links.Remove(a.ID, b.ID, rel)
	if n == 0 {
		if derived := g.LinksBetween(a.ID, b.ID); len(derived) > 0 {
			return i18n.T("graph.unlink.derived", describeNode(a), describeNode(b), linkOrigins(derived)), nil
		}
		return i18n.T("graph.unlink.none", describeNode(a), describeNode(b)), nil
	}
	cli.invalidateMemoryPrompt()
	return i18n.T("graph.unlink.removed", n, describeNode(a), describeNode(b)), nil
}

// normalizeLinkRel maps a free-form relation onto the canonical vocabulary
// when it names one, else keeps the normalized custom slug.
func normalizeLinkRel(rel string) string {
	slug := memory.NormalizeRel(rel)
	switch slug {
	case "mention", "mentions":
		return string(knowledge.RelMentions)
	case "link", "links", "linksto", "links_to", "refers_to", "references":
		return string(knowledge.RelLinksTo)
	case "part", "partof", "part_of", "belongs_to", "in":
		return string(knowledge.RelPartOf)
	case "project", "in_project", "of_project":
		return string(knowledge.RelInProject)
	}
	return slug
}

func describeNode(n *knowledge.Node) string {
	title := strings.TrimSpace(n.Title)
	if title == "" {
		title = n.ID
	}
	return "[" + string(n.Kind) + "] " + title
}

func linkOrigins(ls []knowledge.Link) string {
	seen := map[string]bool{}
	var out []string
	for _, l := range ls {
		label := string(l.Rel) + "/" + string(l.Origin)
		if !seen[label] {
			seen[label] = true
			out = append(out, label)
		}
	}
	return strings.Join(out, ", ")
}

// invalidateMemoryPrompt refreshes the cached prompt context after a link
// mutation, so the next turn's graph card and related-notes section see it.
func (cli *ChatCLI) invalidateMemoryPrompt() {
	if cli.contextBuilder != nil {
		cli.contextBuilder.InvalidateCache()
	}
}
