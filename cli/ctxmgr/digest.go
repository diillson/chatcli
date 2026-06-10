/*
 * ChatCLI - Knowledge index card (digest) builder.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * A knowledge context never injects its corpus: attaching one puts only this
 * digest in the system prompt — a stable, budget-bounded table of contents
 * that tells the model WHAT the knowledge base covers and HOW to reach it
 * (passages are auto-retrieved per turn; agent/coder can additionally pull on
 * demand). A 6MB corpus and a 60MB corpus cost the same handful of tokens per
 * turn. The output is deterministic for a given context, so it lives in the
 * cached prompt prefix without busting provider caches.
 */
package ctxmgr

import (
	"fmt"
	"sort"
	"strings"
)

const (
	// defaultDigestBudget bounds the rendered digest (~4 chars/token ≈ 900
	// tokens) — large enough for a useful TOC, small enough to be a rounding
	// error against the corpus it replaces.
	defaultDigestBudget = 3600

	// digestMaxTitleChars truncates an individual document title in the TOC.
	digestMaxTitleChars = 72
)

// digestSource is one source document aggregated from its chunks.
type digestSource struct {
	path   string
	chunks int
	bytes  int64
	title  string
}

// BuildKnowledgeDigest renders the index card for a knowledge context. budget
// caps the output in bytes; <=0 takes the default. The model-facing scaffolding
// is literal English on purpose (prompt text, not UI).
func BuildKnowledgeDigest(fc *FileContext, budget int) string {
	if fc == nil {
		return ""
	}
	if budget <= 0 {
		budget = defaultDigestBudget
	}
	sources := aggregateSources(fc)

	var b strings.Builder
	fmt.Fprintf(&b, "📚 KNOWLEDGE BASE: %s\n", fc.Name)
	if d := strings.TrimSpace(fc.Description); d != "" {
		fmt.Fprintf(&b, "Description: %s\n", d)
	}
	if repo := fc.Metadata[knowledgeMetaRepoURL]; repo != "" {
		commit := fc.Metadata[knowledgeMetaCommit]
		if len(commit) > 12 {
			commit = commit[:12]
		}
		fmt.Fprintf(&b, "Origin: %s @ %s\n", repo, commit)
	}
	fmt.Fprintf(&b, "Scale: %d document(s), %d passage(s), ~%s tokens of source material (NOT in context)\n",
		len(sources), fc.FileCount, approxTokens(fc.TotalSize))
	b.WriteString("This is an index card only. Relevant passages are auto-retrieved into the ")
	b.WriteString("conversation each turn; answer from those passages and cite their source paths. ")
	b.WriteString("If coverage looks thin, say what additional section of this index you need.\n")
	b.WriteString("\nTable of contents:\n")

	header := b.Len()
	writeDigestTree(&b, sources, budget-header)
	return b.String()
}

// aggregateSources folds per-chunk virtual files back into their source
// documents, deterministically ordered by path.
func aggregateSources(fc *FileContext) []digestSource {
	byPath := make(map[string]*digestSource)
	for _, f := range fc.Files {
		src := chunkSource(f.Path)
		s, ok := byPath[src]
		if !ok {
			s = &digestSource{path: src, title: firstHeading(f.Content)}
			byPath[src] = s
		}
		s.chunks++
		s.bytes += f.Size
		if s.title == "" {
			s.title = firstHeading(f.Content)
		}
	}
	out := make([]digestSource, 0, len(byPath))
	for _, s := range byPath {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

// writeDigestTree renders one line per source until the budget runs out, then
// summarizes what was omitted — silent truncation would read as full coverage.
func writeDigestTree(b *strings.Builder, sources []digestSource, budget int) {
	written := 0
	for i, s := range sources {
		line := digestLine(s)
		if budget > 0 && b.Len()+len(line) > budget {
			fmt.Fprintf(b, "… and %d more document(s) not listed (still searchable).\n", len(sources)-i)
			return
		}
		b.WriteString(line)
		written++
	}
	if written == 0 {
		b.WriteString("(empty corpus)\n")
	}
}

// digestLine renders one TOC entry: path, passage count and title when known.
func digestLine(s digestSource) string {
	title := strings.TrimSpace(s.title)
	if len(title) > digestMaxTitleChars {
		title = title[:digestMaxTitleChars-1] + "…"
	}
	if title != "" {
		return fmt.Sprintf("- %s (%d passages) — %s\n", s.path, s.chunks, title)
	}
	return fmt.Sprintf("- %s (%d passages)\n", s.path, s.chunks)
}

// firstHeading extracts the first markdown heading of a chunk as a
// human-readable title. Scans a bounded prefix only; arbitrary prose lines
// make noisy titles, so no heading means no title.
func firstHeading(content string) string {
	for _, l := range strings.SplitN(content, "\n", 12) {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "#") {
			return strings.TrimSpace(strings.TrimLeft(l, "# "))
		}
	}
	return ""
}

// approxTokens renders the standard 4-chars-per-token estimate with K/M units.
func approxTokens(bytes int64) string {
	t := bytes / 4
	switch {
	case t >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(t)/1_000_000)
	case t >= 1_000:
		return fmt.Sprintf("%.1fK", float64(t)/1_000)
	default:
		return fmt.Sprintf("%d", t)
	}
}
