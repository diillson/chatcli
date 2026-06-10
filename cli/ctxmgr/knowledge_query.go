/*
 * ChatCLI - Knowledge-base query surface for the @knowledge tool.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * PR 1 made knowledge contexts cheap to attach (index card + per-turn push);
 * this file is the pull side: the manager methods the @knowledge tool uses so
 * the agent can interrogate an attached corpus on demand — search passages,
 * read a whole source document, walk the table of contents. Everything is
 * budget-bounded and works keyless (hybrid retrieval has a BM25 floor).
 */
package ctxmgr

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

const (
	// docPageChars bounds one `get` page so a single tool call can never blow
	// the conversation (~3K tokens per page).
	docPageChars = 12_000

	// tocMaxEntries bounds a `toc` listing; prefix filters narrow the rest.
	tocMaxEntries = 200
)

// KnowledgeHit is one retrieved passage tagged with its knowledge base.
type KnowledgeHit struct {
	ContextName string
	Seg         Segment
}

// AttachedKnowledge returns the knowledge-mode contexts attached to the
// session, sorted by name for deterministic listings.
func (m *Manager) AttachedKnowledge(sessionID string) []*FileContext {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []*FileContext
	for _, a := range m.attachedContexts[sessionID] {
		if c, ok := m.contexts[a.ContextID]; ok && c.Mode == ModeKnowledge {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// knowledgeTargets resolves which attached knowledge bases a tool call aims
// at: all of them by default, or the one matching kb (case-insensitive).
func (m *Manager) knowledgeTargets(sessionID, kb string) ([]*FileContext, error) {
	kbs := m.AttachedKnowledge(sessionID)
	if len(kbs) == 0 {
		return nil, fmt.Errorf("no knowledge base attached to this session — attach one with /context attach <name> (mode knowledge)")
	}
	if strings.TrimSpace(kb) == "" {
		return kbs, nil
	}
	for _, c := range kbs {
		if strings.EqualFold(c.Name, kb) {
			return []*FileContext{c}, nil
		}
	}
	names := make([]string, 0, len(kbs))
	for _, c := range kbs {
		names = append(names, c.Name)
	}
	return nil, fmt.Errorf("knowledge base %q not attached (attached: %s)", kb, strings.Join(names, ", "))
}

// KnowledgeSearch runs hybrid retrieval over the attached knowledge bases
// (one of them when kb is set) and returns up to k passages per base.
func (m *Manager) KnowledgeSearch(ctx context.Context, sessionID, kb, query string, k int) ([]KnowledgeHit, error) {
	targets, err := m.knowledgeTargets(sessionID, kb)
	if err != nil {
		return nil, err
	}
	if k <= 0 {
		k = DefaultRetrievalTopK
	}
	m.mu.RLock()
	engine := m.retrieval
	m.mu.RUnlock()
	if engine == nil {
		return nil, fmt.Errorf("knowledge retrieval engine unavailable")
	}
	var hits []KnowledgeHit
	for _, fc := range targets {
		segs, err := engine.RetrieveHybrid(ctx, fc, query, k)
		if err != nil {
			m.logger.Warn("knowledge search failed for context; skipping")
			continue
		}
		for _, s := range segs {
			hits = append(hits, KnowledgeHit{ContextName: fc.Name, Seg: s})
		}
	}
	return hits, nil
}

// KnowledgeDocument returns one page of a source document (all chunks whose
// source matches, in corpus order), plus pagination info. offset is a
// character offset into the assembled document; the next offset is returned
// when more content remains (0 = done).
func (m *Manager) KnowledgeDocument(sessionID, kb, source string, offset int) (page string, total int, nextOffset int, err error) {
	targets, err := m.knowledgeTargets(sessionID, kb)
	if err != nil {
		return "", 0, 0, err
	}
	source = strings.TrimSpace(source)
	if source == "" {
		return "", 0, 0, fmt.Errorf("source is required — use toc to list document paths")
	}
	for _, fc := range targets {
		var parts []string
		for _, f := range fc.Files {
			if strings.EqualFold(chunkSource(f.Path), source) {
				parts = append(parts, f.Content)
			}
		}
		if len(parts) == 0 {
			continue
		}
		doc := strings.Join(parts, "\n\n")
		total = len(doc)
		if offset < 0 {
			offset = 0
		}
		if offset >= total {
			return "", total, 0, fmt.Errorf("offset %d beyond document end (%d chars)", offset, total)
		}
		end := offset + docPageChars
		if end > total {
			end = total
		}
		next := 0
		if end < total {
			next = end
		}
		return doc[offset:end], total, next, nil
	}
	return "", 0, 0, fmt.Errorf("document %q not found in the attached knowledge base(s) — check toc for exact paths", source)
}

// KnowledgeTOC lists the source documents of the attached knowledge bases,
// optionally filtered by a path prefix. Rendering is model-facing English,
// consistent with the other prompt scaffolding in this package.
func (m *Manager) KnowledgeTOC(sessionID, kb, prefix string) (string, error) {
	targets, err := m.knowledgeTargets(sessionID, kb)
	if err != nil {
		return "", err
	}
	prefix = strings.TrimSpace(prefix)
	var b strings.Builder
	for _, fc := range targets {
		sources := aggregateSources(fc)
		if prefix != "" {
			filtered := sources[:0]
			for _, s := range sources {
				if strings.HasPrefix(strings.ToLower(s.path), strings.ToLower(prefix)) {
					filtered = append(filtered, s)
				}
			}
			sources = filtered
		}
		fmt.Fprintf(&b, "📚 %s — %d document(s)", fc.Name, len(sources))
		if prefix != "" {
			fmt.Fprintf(&b, " matching %q", prefix)
		}
		b.WriteString("\n")
		for i, s := range sources {
			if i >= tocMaxEntries {
				fmt.Fprintf(&b, "… and %d more — narrow with a prefix.\n", len(sources)-i)
				break
			}
			b.WriteString(digestLine(s))
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// FormatKnowledgeHits renders search results for the tool transcript: each
// passage cites its knowledge base, source path and position so the model can
// follow up with `get` on the exact document.
func FormatKnowledgeHits(query string, hits []KnowledgeHit) string {
	if len(hits) == 0 {
		return "No passages matched. Try different terms, or use toc to inspect what the knowledge base covers."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d passage(s) for %q:\n\n", len(hits), query)
	for i, h := range hits {
		fmt.Fprintf(&b, "[%d] %s :: %s (lines %d-%d)\n", i+1, h.ContextName, h.Seg.FilePath, h.Seg.StartLine, h.Seg.EndLine)
		b.WriteString("```\n")
		b.WriteString(h.Seg.Content)
		b.WriteString("\n```\n\n")
	}
	b.WriteString("Use {\"cmd\":\"get\",\"args\":{\"source\":\"<path before the #>\"}} to read a full document.")
	return b.String()
}
