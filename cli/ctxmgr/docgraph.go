/*
 * ChatCLI - Document graph of a context (the vault view of /context).
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * A context stores files (or, for a docs-flatten corpus, chunks whose path
 * is "source#nnnn"). The vault model needs the level a person thinks in: the
 * DOCUMENT. Documents folds a context's files into one entry per source
 * document — title, a one-line summary, and the links the document itself
 * writes:
 *
 *   - relative markdown links ([text](../guide/setup.md#step), /docs/x),
 *     resolved against the document's own path inside the context;
 *   - [[wikilinks]], resolved against the context's document titles and
 *     file names first; the ones that name nothing in the context are kept
 *     for the knowledge graph to resolve against the rest of the vault
 *     (a doc may link to a memory topic or project).
 *
 * The derivation is O(corpus), so it is memoized per context revision (the
 * same fingerprint the retrieval caches use) and dropped on delete.
 */
package ctxmgr

import (
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/diillson/chatcli/pkg/knowledge"
	"github.com/diillson/chatcli/utils"
)

const (
	// maxDocumentsPerContext bounds the documents one context contributes to
	// the graph; the rest are still searchable through retrieval.
	maxDocumentsPerContext = 2000
	// docSummaryMaxRunes bounds a document's one-line summary.
	docSummaryMaxRunes = 200
	// maxLinksPerDocument bounds the outgoing links kept per document.
	maxLinksPerDocument = 64
)

// Document is one source document of a context, as the vault sees it.
type Document struct {
	// Source is the document path inside the context ("docs/intro.md").
	Source string
	// Title is the first heading (or front-matter title), else the file
	// name without extension.
	Title string
	// Summary is the first prose line, bounded.
	Summary string
	// Type is the file type tag of the first chunk ("md", "go", …).
	Type string
	// Size is the document's total content size in bytes.
	Size int64
	// Chunks is how many stored files/chunks the document spans.
	Chunks int
	// Links are the sources of other documents in the same context this
	// document links to (markdown links and resolved wikilinks), sorted.
	Links []string
	// Wikilinks are the [[targets]] that name no document of this context,
	// left for vault-wide resolution, in order of appearance.
	Wikilinks []string
	// Truncated is true when the context had more documents than the cap
	// (set on the last returned document only).
	Truncated bool
}

type documentsEntry struct {
	fingerprint string
	docs        []Document
	// files is the context's file slice as of this revision (contexts swap
	// in a new slice on update, never mutate one in place) and chunks maps
	// every source — beyond the document cap too — to its file indexes in
	// corpus order, so assembling one document never rescans the corpus.
	files  []utils.FileInfo
	chunks map[string][]int
}

// snapshotContext copies a context's header under the manager lock: the
// struct is updated in place by /context update and refresh, and readers
// here run off the turn goroutine (the vault syncer).
func (m *Manager) snapshotContext(id string) (FileContext, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	fc, ok := m.contexts[id]
	if !ok || fc == nil {
		return FileContext{}, false
	}
	return cloneContextHeader(fc), true
}

// cloneContextHeader copies a context for lock-free reading: the struct,
// plus the maps refresh and update write in place (Metadata holds the
// index seals the fingerprint reads; FileStamps is rewritten too). Slices
// are swapped wholesale on update, never mutated, so sharing them is safe.
func cloneContextHeader(fc *FileContext) FileContext {
	cp := *fc
	if fc.Metadata != nil {
		cp.Metadata = make(map[string]string, len(fc.Metadata))
		for k, v := range fc.Metadata {
			cp.Metadata[k] = v
		}
	}
	if fc.FileStamps != nil {
		cp.FileStamps = make(map[string]FileStamp, len(fc.FileStamps))
		for k, v := range fc.FileStamps {
			cp.FileStamps[k] = v
		}
	}
	return cp
}

// SnapshotContexts returns shallow copies of every context (oldest first),
// taken under the manager lock — the race-free listing for background
// readers such as the knowledge graph and the vault.
func (m *Manager) SnapshotContexts() []FileContext {
	m.mu.RLock()
	out := make([]FileContext, 0, len(m.contexts))
	for _, fc := range m.contexts {
		if fc != nil {
			out = append(out, cloneContextHeader(fc))
		}
	}
	m.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// documentsEntryFor returns the memoized document set of a context's
// current revision, building it when stale.
func (m *Manager) documentsEntryFor(contextID string) (documentsEntry, bool) {
	fc, ok := m.snapshotContext(contextID)
	if !ok {
		return documentsEntry{}, false
	}
	fp := contextFingerprint(&fc)
	m.docsMu.Lock()
	if e, ok := m.docsCache[contextID]; ok && e.fingerprint == fp {
		m.docsMu.Unlock()
		return e, true
	}
	m.docsMu.Unlock()

	docs, chunks := foldDocuments(&fc)
	e := documentsEntry{fingerprint: fp, docs: docs, files: fc.Files, chunks: chunks}
	m.docsMu.Lock()
	if m.docsCache == nil {
		m.docsCache = make(map[string]documentsEntry)
	}
	m.docsCache[contextID] = e
	m.docsMu.Unlock()
	return e, true
}

// Documents returns the documents of a context (nil for an unknown id),
// memoized per context revision. The returned slice is shared: callers must
// not mutate it.
func (m *Manager) Documents(contextID string) []Document {
	e, ok := m.documentsEntryFor(contextID)
	if !ok {
		return nil
	}
	return e.docs
}

// DocumentLinksByName returns, for one document of the context named name,
// the documents it links to and the documents linking to it (both sorted).
// Unknown context or document yields nil slices.
func (m *Manager) DocumentLinksByName(name, source string) (out, in []string) {
	m.mu.RLock()
	var id string
	for cid, c := range m.contexts {
		if strings.EqualFold(c.Name, name) {
			id = cid
			break
		}
	}
	m.mu.RUnlock()
	if id == "" {
		return nil, nil
	}
	docs := m.Documents(id)
	for _, d := range docs {
		if strings.EqualFold(d.Source, source) {
			out = append(out, d.Links...)
		}
		for _, l := range d.Links {
			if strings.EqualFold(l, source) {
				in = append(in, d.Source)
				break
			}
		}
	}
	sort.Strings(in)
	return out, in
}

// HasDocument reports whether the context holds a document with this
// source (any chunk of it), independent of the graph's document caps.
func (m *Manager) HasDocument(contextID, source string) bool {
	e, ok := m.documentsEntryFor(contextID)
	return ok && len(e.chunks[source]) > 0
}

// DocumentContent assembles one document of a context from its chunks, in
// corpus order, joined exactly like the knowledge doc reader does. ""
// when the context or document is unknown.
func (m *Manager) DocumentContent(contextID, source string) string {
	e, ok := m.documentsEntryFor(contextID)
	if !ok {
		return ""
	}
	idx := e.chunks[source]
	parts := make([]string, 0, len(idx))
	for _, i := range idx {
		parts = append(parts, e.files[i].Content)
	}
	return strings.Join(parts, "\n\n")
}

// DocumentsFingerprint is the revision key of a context's documents: it
// changes whenever their content can have changed (same key the retrieval
// caches use).
func (m *Manager) DocumentsFingerprint(contextID string) string {
	fc, ok := m.snapshotContext(contextID)
	if !ok {
		return ""
	}
	return contextFingerprint(&fc)
}

// knowledgeLinkHintMax bounds the linked-document hints appended to one
// knowledge search: a nudge toward neighbors, not a second result list.
const knowledgeLinkHintMax = 6

// KnowledgeLinkHints renders the documents linked with (to or from) the
// documents the top hits came from, minus those already among the hits —
// the "see also" a person follows in a wiki. "" when there is none.
func (m *Manager) KnowledgeLinkHints(hits []KnowledgeHit) string {
	type key struct{ kb, src string }
	hit := make(map[key]bool, len(hits))
	var seeds []key
	for _, h := range hits {
		k := key{h.ContextName, chunkSource(h.Seg.FilePath)}
		if !hit[k] {
			hit[k] = true
			seeds = append(seeds, k)
		}
	}
	if len(seeds) > 3 {
		seeds = seeds[:3]
	}
	var lines []string
	seen := make(map[key]bool)
	add := func(k key, arrow string, from string) {
		if hit[k] || seen[k] || len(lines) >= knowledgeLinkHintMax {
			return
		}
		seen[k] = true
		lines = append(lines, fmt.Sprintf("- %s :: %s (%s %s)", k.kb, k.src, arrow, from))
	}
	for _, s := range seeds {
		out, in := m.DocumentLinksByName(s.kb, s.src)
		for _, o := range out {
			add(key{s.kb, o}, "linked from", s.src)
		}
		for _, i := range in {
			add(key{s.kb, i}, "links to", s.src)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "Linked documents (not in the results above; read with get, or links for more):\n" + strings.Join(lines, "\n")
}

// KnowledgeLinks renders one document's links and backlinks within its
// knowledge base, with titles — the @knowledge links answer.
func (m *Manager) KnowledgeLinks(sessionID, kb, source string) (string, error) {
	targets, err := m.knowledgeTargets(sessionID, kb)
	if err != nil {
		return "", err
	}
	source = strings.TrimSpace(source)
	if i := strings.LastIndex(source, "#"); i > 0 {
		source = source[:i] // a cited chunk id names its document
	}
	if source == "" {
		return "", fmt.Errorf("source is required — a document path from search/toc")
	}
	for _, fc := range targets {
		docs := m.Documents(fc.ID)
		titles := make(map[string]string, len(docs))
		var doc *Document
		for i := range docs {
			titles[docs[i].Source] = docs[i].Title
			if strings.EqualFold(docs[i].Source, source) {
				doc = &docs[i]
			}
		}
		if doc == nil {
			continue
		}
		out, in := m.DocumentLinksByName(fc.Name, doc.Source)
		var b strings.Builder
		fmt.Fprintf(&b, "%s :: %s — %q\n", fc.Name, doc.Source, doc.Title)
		if doc.Summary != "" {
			fmt.Fprintf(&b, "  · %s\n", doc.Summary)
		}
		list := func(label string, srcs []string) {
			if len(srcs) == 0 {
				fmt.Fprintf(&b, "%s: none\n", label)
				return
			}
			fmt.Fprintf(&b, "%s (%d):\n", label, len(srcs))
			for _, s := range srcs {
				fmt.Fprintf(&b, "  - %s — %q\n", s, titles[s])
			}
		}
		list("→ links to", out)
		list("← linked from", in)
		if len(doc.Wikilinks) > 0 {
			fmt.Fprintf(&b, "[[wikilinks]] to notes outside this base (see @memory neighbors): %s\n", strings.Join(doc.Wikilinks, ", "))
		}
		return strings.TrimRight(b.String(), "\n"), nil
	}
	return "", fmt.Errorf("document %q not found in the attached knowledge base(s) — check toc for exact paths", source)
}

func (m *Manager) dropDocuments(contextID string) {
	m.docsMu.Lock()
	delete(m.docsCache, contextID)
	m.docsMu.Unlock()
}

// BuildDocuments folds a context's files into documents and resolves their
// links. Deterministic: documents come back sorted by source.
func BuildDocuments(fc *FileContext) []Document {
	docs, _ := foldDocuments(fc)
	return docs
}

// foldDocuments is BuildDocuments plus the source → file-index map of
// every source (the memo's content index).
func foldDocuments(fc *FileContext) ([]Document, map[string][]int) {
	if fc == nil || len(fc.Files) == 0 {
		return nil, nil
	}
	type acc struct {
		doc    Document
		chunks []int // indexes into fc.Files, corpus order
	}
	bySource := make(map[string]*acc)
	var order []string
	for i, f := range fc.Files {
		src := chunkSource(f.Path)
		a, ok := bySource[src]
		if !ok {
			a = &acc{doc: Document{Source: src, Type: f.Type}}
			bySource[src] = a
			order = append(order, src)
		}
		a.chunks = append(a.chunks, i)
		a.doc.Size += int64(len(f.Content))
	}
	sort.Strings(order)
	chunkIdx := make(map[string][]int, len(order))
	for _, src := range order {
		chunkIdx[src] = bySource[src].chunks
	}
	truncated := false
	if len(order) > maxDocumentsPerContext {
		order = order[:maxDocumentsPerContext]
		truncated = true
	}

	// Name index for wikilink resolution inside the context: file name
	// without extension, and (below) the title.
	names := make(map[string]string, len(order)*2)
	addName := func(name, src string) {
		key := knowledge.NormalizeRef(name)
		if key == "" {
			return
		}
		if cur, ok := names[key]; !ok || src < cur {
			names[key] = src
		}
	}
	// Path index for markdown links: the source, its extension-less form,
	// and README/index folding to the directory.
	paths := make(map[string]string, len(order)*3)
	for _, src := range order {
		a := bySource[src]
		a.doc.Chunks = len(a.chunks)
		first := fc.Files[a.chunks[0]].Content
		a.doc.Title, a.doc.Summary = documentHeadline(first, src)
		addName(baseNoExt(src), src)
		for _, k := range pathKeys(src) {
			if _, taken := paths[k]; !taken {
				paths[k] = src
			}
		}
	}
	for _, src := range order {
		addName(bySource[src].doc.Title, src)
	}

	docs := make([]Document, 0, len(order))
	for _, src := range order {
		a := bySource[src]
		links := make(map[string]bool)
		var wiki []string
		wikiSeen := make(map[string]bool)
		if !linksParsed(src) {
			docs = append(docs, a.doc) // code and data files write no links
			continue
		}
		for _, ci := range a.chunks {
			// Links inside fenced code are examples, not references.
			content := stripFencedCode(fc.Files[ci].Content)
			for _, target := range markdownLinkTargets(content) {
				if dst, ok := resolveDocPath(src, target, paths); ok && dst != src {
					links[dst] = true
				}
			}
			for _, target := range knowledge.ParseWikilinks(content) {
				if dst, ok := names[knowledge.NormalizeRef(target)]; ok {
					if dst != src {
						links[dst] = true
					}
					continue
				}
				if !wikiSeen[target] {
					wikiSeen[target] = true
					wiki = append(wiki, target)
				}
			}
		}
		for l := range links {
			a.doc.Links = append(a.doc.Links, l)
		}
		sort.Strings(a.doc.Links)
		if len(a.doc.Links) > maxLinksPerDocument {
			a.doc.Links = a.doc.Links[:maxLinksPerDocument]
		}
		if len(wiki) > maxLinksPerDocument {
			wiki = wiki[:maxLinksPerDocument]
		}
		a.doc.Wikilinks = wiki
		docs = append(docs, a.doc)
	}
	if truncated && len(docs) > 0 {
		docs[len(docs)-1].Truncated = true
	}
	return docs, chunkIdx
}

// linksParsed reports whether a source is prose whose links mean
// something: Markdown (and plain text). In code, "[[ -f x ]]" is shell and
// "](" is an expression.
func linksParsed(src string) bool {
	switch strings.ToLower(path.Ext(slashed(src))) {
	case ".md", ".mdx", ".markdown", ".txt", "":
		return true
	}
	return false
}

// stripFencedCode blanks ``` / ~~~ fenced blocks (keeping line structure).
func stripFencedCode(content string) string {
	if !strings.Contains(content, "```") && !strings.Contains(content, "~~~") {
		return content
	}
	lines := strings.Split(content, "\n")
	in := false
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			in = !in
			lines[i] = ""
			continue
		}
		if in {
			lines[i] = ""
		}
	}
	return strings.Join(lines, "\n")
}

// documentHeadline extracts the title and a one-line summary from the first
// chunk of a document.
func documentHeadline(content, src string) (title, summary string) {
	lines := strings.Split(content, "\n")
	inFront, inFence := false, false
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if i == 0 && line == "---" {
			inFront = true
			continue
		}
		if inFront {
			if line == "---" {
				inFront = false
				continue
			}
			if title == "" {
				if v, ok := frontMatterValue(line, "title"); ok {
					title = v
				}
			}
			continue
		}
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence || line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			h := strings.TrimSpace(strings.TrimLeft(line, "#"))
			if title == "" && h != "" {
				title = h
			}
			continue
		}
		if summary == "" && !strings.HasPrefix(line, "<") && !strings.HasPrefix(line, "import ") &&
			!strings.HasPrefix(line, "|") && !strings.HasPrefix(line, "!") {
			summary = line
		}
		if title != "" && summary != "" {
			break
		}
	}
	if title == "" {
		title = baseNoExt(src)
	}
	return strings.TrimSpace(title), truncateRunesDoc(summary, docSummaryMaxRunes)
}

func frontMatterValue(line, key string) (string, bool) {
	k, v, ok := strings.Cut(line, ":")
	if !ok || !strings.EqualFold(strings.TrimSpace(k), key) {
		return "", false
	}
	v = strings.Trim(strings.TrimSpace(v), `"'`)
	return v, v != ""
}

// markdownLinkTargets returns the targets of inline markdown links
// ([text](target "title")) that point inside the corpus: absolute URLs,
// mailto:, data: and in-page anchors are skipped.
func markdownLinkTargets(content string) []string {
	var out []string
	for {
		i := strings.Index(content, "](")
		if i < 0 {
			return out
		}
		rest := content[i+2:]
		end := strings.IndexByte(rest, ')')
		if end < 0 {
			return out
		}
		target := strings.TrimSpace(rest[:end])
		content = rest[end+1:]
		if sp := strings.IndexAny(target, " \t"); sp >= 0 {
			target = target[:sp] // drop the optional "title"
		}
		target = strings.Trim(target, "<>")
		if target == "" || strings.HasPrefix(target, "#") || strings.ContainsAny(target, "\n") {
			continue
		}
		lower := strings.ToLower(target)
		if strings.Contains(lower, "://") || strings.HasPrefix(lower, "mailto:") ||
			strings.HasPrefix(lower, "data:") || strings.HasPrefix(lower, "tel:") {
			continue
		}
		out = append(out, target)
	}
}

// resolveDocPath resolves a markdown link target written in document src to
// another document of the context.
func resolveDocPath(src, target string, paths map[string]string) (string, bool) {
	if i := strings.IndexAny(target, "#?"); i >= 0 {
		target = target[:i]
	}
	if dec, err := url.PathUnescape(target); err == nil {
		target = dec
	}
	if target == "" {
		return "", false
	}
	target = slashed(target)
	var cand string
	if strings.HasPrefix(target, "/") {
		cand = path.Clean(strings.TrimPrefix(target, "/"))
	} else {
		cand = path.Clean(path.Join(path.Dir(slashed(src)), target))
	}
	for _, k := range pathKeys(cand) {
		if dst, ok := paths[k]; ok {
			return dst, true
		}
	}
	// Site-absolute links ("/guide/setup") often omit a docs root the
	// corpus keeps ("docs/guide/setup.md"): accept a unique suffix match.
	if strings.HasPrefix(target, "/") {
		want := "/" + strings.ToLower(trimDocExt(cand))
		var hit string
		for k, dst := range paths {
			if strings.HasSuffix("/"+k, want) {
				if hit != "" && hit != dst {
					return "", false
				}
				hit = dst
			}
		}
		if hit != "" {
			return hit, true
		}
	}
	return "", false
}

// pathKeys are the lookup keys a document path answers to: itself, without
// its doc extension, and its directory when it is a README/index page.
func pathKeys(p string) []string {
	p = strings.ToLower(path.Clean(strings.TrimPrefix(slashed(p), "./")))
	keys := []string{p}
	noExt := trimDocExt(p)
	if noExt != p {
		keys = append(keys, noExt)
	}
	base := path.Base(noExt)
	if base == "readme" || base == "index" {
		keys = append(keys, path.Dir(noExt))
	}
	return keys
}

// slashed normalizes Windows separators: contexts scanned on Windows store
// backslash paths, while links inside documents always use '/'.
func slashed(p string) string { return strings.ReplaceAll(p, "\\", "/") }

func trimDocExt(p string) string {
	for _, ext := range []string{".mdx", ".md", ".markdown", ".html", ".htm"} {
		if strings.HasSuffix(strings.ToLower(p), ext) {
			return p[:len(p)-len(ext)]
		}
	}
	return p
}

func baseNoExt(p string) string {
	b := path.Base(slashed(p))
	if i := strings.LastIndexByte(b, '.'); i > 0 {
		b = b[:i]
	}
	return b
}

func truncateRunesDoc(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n])) + "…"
}
