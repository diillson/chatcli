/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package compress

import (
	"fmt"
	"strings"
)

// ProseCompressor reduces prose / Markdown — the HTML-turned-Markdown an agent
// gets back from web fetches and searches. That content is dominated by
// boilerplate: navigation menus, cookie banners, "skip to content", footers
// repeated across every page, plus long runs of blank lines. This compressor
// strips the repetition keylessly (no ML model, unlike Headroom's Kompress) and
// offloads the full original to CCR.
//
// It is deliberately conservative — it removes only *exact duplicate* non-empty
// lines (keeping the first occurrence) and collapses blank-line runs, then
// trims only individual sections that are extraordinarily long. Unique prose is
// never dropped.
//
// SCOPE — to honor "never degrade", prose compression auto-fires ONLY on
// reference material fetched from the web (Detect keys off the web tool hints),
// or when explicitly requested via Hint.MIME=="prose"/"markdown". It never
// engages on local file reads (@read), which an agent may be about to edit.
type ProseCompressor struct {
	// SectionCap is the max chars kept for a single section before head/tail
	// trimming kicks in. Sections shorter than this are kept whole.
	SectionCap int
}

// NewProseCompressor returns a compressor with sensible defaults.
func NewProseCompressor() *ProseCompressor {
	return &ProseCompressor{SectionCap: 6000}
}

// Name implements Compressor.
func (*ProseCompressor) Name() string { return "prose" }

// Detect implements Compressor. It engages on web/reference tool output and on
// an explicit prose/markdown hint — never by sniffing arbitrary text, so it
// can't silently rewrite content an agent is editing.
func (c *ProseCompressor) Detect(_ string, h Hint) float64 {
	switch strings.ToLower(strings.TrimSpace(h.MIME)) {
	case "prose", "markdown", "md", "text", "html":
		return 0.92
	}
	switch strings.ToLower(strings.TrimSpace(h.ToolName)) {
	case "@webfetch", "@websearch", "@wikipedia", "webfetch", "websearch":
		return 0.7
	}
	return 0
}

// Compress implements Compressor.
func (c *ProseCompressor) Compress(content string, opts Options) (Result, error) {
	if !canDrop(opts) {
		// Dedup/trim is lossy; without CCR we keep the content intact.
		return passthrough(content), nil
	}

	reduced, removed := c.reduce(content)
	if removed == 0 || len(reduced) >= len(content) {
		return passthrough(content), nil
	}

	marker, key := offload(content, opts)
	if marker == "" {
		return passthrough(content), nil
	}
	out := reduced + fmt.Sprintf("\n\n[prose: %d duplicate/boilerplate line(s) removed — full text recoverable with @recall %s]\n", removed, marker)
	return Result{
		Compressed:     out,
		OriginalSize:   len(content),
		CompressedSize: len(out),
		Strategy:       c.Name(),
		CacheKey:       key,
		Reversible:     true,
		Detail:         map[string]int{"lines_removed": removed},
	}, nil
}

// reduce removes exact-duplicate non-empty lines (keeping first occurrence),
// collapses blank-line runs to a single blank, and trims individual sections
// that exceed SectionCap. Returns the reduced text and the count of removed
// lines.
func (c *ProseCompressor) reduce(content string) (string, int) {
	lines := strings.Split(content, "\n")
	seen := make(map[string]struct{}, len(lines))
	kept := make([]string, 0, len(lines))
	removed := 0
	blankRun := 0

	for _, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		if trimmed == "" {
			blankRun++
			if blankRun > 1 {
				removed++
				continue // collapse consecutive blanks
			}
			kept = append(kept, "")
			continue
		}
		blankRun = 0

		// Dedup only "boilerplate-ish" lines: short-to-medium lines that recur
		// verbatim. Long unique paragraphs are never deduped (they won't recur
		// anyway, but the length guard avoids dropping a legitimately repeated
		// long quote).
		if len(trimmed) <= 200 {
			if _, dup := seen[trimmed]; dup {
				removed++
				continue
			}
			seen[trimmed] = struct{}{}
		}
		kept = append(kept, ln)
	}

	joined := strings.Join(kept, "\n")
	trimmedSections, secRemoved := c.trimLongSections(joined)
	return trimmedSections, removed + secRemoved
}

// trimLongSections head/tail-trims any section (text between Markdown headings)
// that exceeds SectionCap. Headings are kept so the document outline survives.
func (c *ProseCompressor) trimLongSections(content string) (string, int) {
	if c.SectionCap <= 0 {
		return content, 0
	}
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	section := make([]string, 0, len(lines))
	removed := 0

	flush := func() {
		if len(section) == 0 {
			return
		}
		body := strings.Join(section, "\n")
		if len(body) <= c.SectionCap {
			out = append(out, section...)
		} else {
			head := c.SectionCap * 2 / 3
			tail := c.SectionCap / 3
			before := len(strings.Split(body, "\n"))
			trimmed := body[:head] + fmt.Sprintf("\n\n... [%d chars of this section omitted] ...\n\n", len(body)-head-tail) + body[len(body)-tail:]
			out = append(out, trimmed)
			removed += before - len(strings.Split(trimmed, "\n"))
		}
		section = nil
	}

	for _, ln := range lines {
		if isMarkdownHeading(ln) {
			flush()
			out = append(out, ln) // heading kept verbatim
			continue
		}
		section = append(section, ln)
	}
	flush()
	return strings.Join(out, "\n"), removed
}

// isMarkdownHeading reports whether a line is an ATX Markdown heading ("# ...").
func isMarkdownHeading(ln string) bool {
	t := strings.TrimLeft(ln, " ")
	if !strings.HasPrefix(t, "#") {
		return false
	}
	i := 0
	for i < len(t) && t[i] == '#' {
		i++
	}
	return i >= 1 && i <= 6 && i < len(t) && t[i] == ' '
}
