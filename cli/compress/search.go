/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package compress

import (
	"fmt"
	"strconv"
	"strings"
)

// SearchCompressor reduces grep/ripgrep output. Code-search results are the
// single highest-volume, highest-redundancy payload an agent reads: hundreds
// of "path:line:content" rows where the model only needs a representative
// sample per file plus anything that looks like an error. This is the
// compressor behind Headroom's headline "100 results 17,765 -> 1,408 tokens".
//
// Parser robustness (the bugs the reference Rust port fixed, ported here):
//   - Windows drive letters: "C:\src\main.go:42:hit" must not treat the drive
//     colon as the line-number separator.
//   - Dashes in filenames: ripgrep context lines use "path-42-content"; a path
//     like "pre-commit-config.yaml" must still parse. We anchor on the
//     earliest "<sep>\d+<sep>" marker rather than a fixed character class.
type SearchCompressor struct {
	MaxMatchesPerFile int
	MaxTotalMatches   int
	MaxFiles          int
}

// NewSearchCompressor returns a compressor with Headroom-equivalent caps.
func NewSearchCompressor() *SearchCompressor {
	return &SearchCompressor{
		MaxMatchesPerFile: 5,
		MaxTotalMatches:   30,
		MaxFiles:          15,
	}
}

// Name implements Compressor.
func (*SearchCompressor) Name() string { return "search" }

type searchMatch struct {
	line    int
	content string
	isErr   bool
}

type fileGroup struct {
	path    string
	matches []searchMatch
}

// Detect implements Compressor. Confidence rises with the fraction of lines
// that parse as "path:line:content" rows.
func (c *SearchCompressor) Detect(content string, h Hint) float64 {
	switch strings.ToLower(strings.TrimSpace(h.ToolName)) {
	case "@search", "grep", "rg", "ripgrep", "ag", "ack":
		return 0.95
	}
	lines := splitLines(content)
	if len(lines) < 3 {
		return 0
	}
	parsed := 0
	for _, ln := range lines {
		// For detection (not parsing) require the path to look like a real
		// file, so log lines with ISO dates ("2026-06-22 ...") or timestamps
		// ("12:00:07") don't masquerade as grep "path:line:content" rows.
		if path, _, _, _, ok := parseSearchLine(ln); ok && pathLooksLikeFile(path) {
			parsed++
		}
	}
	frac := float64(parsed) / float64(len(lines))
	switch {
	case frac >= 0.8:
		return 0.9
	case frac >= 0.6:
		return 0.75
	case frac >= 0.4:
		return 0.55
	default:
		return 0
	}
}

// Compress implements Compressor.
func (c *SearchCompressor) Compress(content string, opts Options) (Result, error) {
	groups, parsedCount := c.parse(content)
	if parsedCount == 0 {
		return passthrough(content), nil
	}

	allowDrop := canDrop(opts)
	kept, keptMatches, droppedMatches, droppedFiles := c.selectGroups(groups, allowDrop)

	var b strings.Builder
	for _, g := range kept {
		b.WriteString(g.path)
		b.WriteByte('\n')
		for _, m := range g.matches {
			fmt.Fprintf(&b, "  L%d: %s\n", m.line, strings.TrimRight(m.content, " \t"))
		}
	}

	cacheKey := ""
	if droppedMatches > 0 || droppedFiles > 0 {
		marker, key := offload(content, opts)
		if marker == "" {
			// CCR unavailable: we must not drop. Fall back to passthrough so
			// nothing is lost.
			return passthrough(content), nil
		}
		cacheKey = key
		fmt.Fprintf(&b, "\n... [search: %d matches", droppedMatches)
		if droppedFiles > 0 {
			fmt.Fprintf(&b, " across %d files", droppedFiles)
		}
		fmt.Fprintf(&b, " omitted — recall full results with @recall %s] ...\n", marker)
	}

	out := b.String()
	return Result{
		Compressed:     out,
		OriginalSize:   len(content),
		CompressedSize: len(out),
		Strategy:       c.Name(),
		CacheKey:       cacheKey,
		Reversible:     true,
		Detail: map[string]int{
			"matches_kept":    keptMatches,
			"matches_dropped": droppedMatches,
			"files_dropped":   droppedFiles,
		},
	}, nil
}

// parse groups parsed match lines by file, preserving first-seen order. Lines
// that don't parse (separators "--", blank lines, banners) are skipped.
func (c *SearchCompressor) parse(content string) ([]*fileGroup, int) {
	var groups []*fileGroup
	index := make(map[string]*fileGroup)
	parsed := 0
	for _, ln := range splitLines(content) {
		path, lineNo, text, _, ok := parseSearchLine(ln)
		if !ok {
			continue
		}
		parsed++
		g := index[path]
		if g == nil {
			g = &fileGroup{path: path}
			index[path] = g
			groups = append(groups, g)
		}
		g.matches = append(g.matches, searchMatch{line: lineNo, content: text, isErr: hasErrorSignal(text)})
	}
	return groups, parsed
}

// select applies the per-file and global caps. When allowDrop is false it
// keeps everything (lossless), so the router will see no reduction and fall
// back to passthrough — exactly the safe behavior. The dropped-match count is
// derived authoritatively as (total original − total kept) to avoid any
// double-counting between the per-file and global budgets.
func (c *SearchCompressor) selectGroups(groups []*fileGroup, allowDrop bool) (kept []*fileGroup, keptMatches, droppedMatches, droppedFiles int) {
	totalKept, totalOriginal := 0, 0
	for fi, g := range groups {
		totalOriginal += len(g.matches)
		if allowDrop && fi >= c.MaxFiles {
			droppedFiles++
			continue
		}
		sel := g.matches
		if allowDrop {
			sel = c.selectMatches(g.matches)
			if room := c.MaxTotalMatches - totalKept; len(sel) > room {
				if room < 0 {
					room = 0
				}
				sel = sel[:room]
			}
		}
		kept = append(kept, &fileGroup{path: g.path, matches: sel})
		totalKept += len(sel)
	}
	keptMatches = totalKept
	droppedMatches = totalOriginal - totalKept
	return kept, keptMatches, droppedMatches, droppedFiles
}

// selectMatches keeps first, last, all error matches, then fills by order up
// to MaxMatchesPerFile.
func (c *SearchCompressor) selectMatches(matches []searchMatch) []searchMatch {
	if len(matches) <= c.MaxMatchesPerFile {
		return matches
	}
	keepIdx := make(map[int]struct{})
	keepIdx[0] = struct{}{}
	keepIdx[len(matches)-1] = struct{}{}
	for i, m := range matches {
		if len(keepIdx) >= c.MaxMatchesPerFile {
			break
		}
		if m.isErr {
			keepIdx[i] = struct{}{}
		}
	}
	for i := range matches {
		if len(keepIdx) >= c.MaxMatchesPerFile {
			break
		}
		keepIdx[i] = struct{}{}
	}
	out := make([]searchMatch, 0, len(keepIdx))
	for i, m := range matches {
		if _, ok := keepIdx[i]; ok {
			out = append(out, m)
		}
	}
	return out
}

// pathLooksLikeFile reports whether s plausibly names a file — it contains a
// path separator or ends in a short dotted extension. Used only to gate search
// *detection* (parseSearchLine itself stays permissive so explicit @search can
// still handle extension-less paths like "Makefile").
func pathLooksLikeFile(s string) bool {
	if strings.ContainsAny(s, "/\\") {
		return true
	}
	if dot := strings.LastIndexByte(s, '.'); dot > 0 && dot < len(s)-1 {
		ext := s[dot+1:]
		if len(ext) >= 1 && len(ext) <= 5 && isAllLetters(ext) {
			return true
		}
	}
	return false
}

func isAllLetters(s string) bool {
	for i := 0; i < len(s); i++ {
		if !isAlpha(s[i]) {
			return false
		}
	}
	return true
}

// parseSearchLine extracts (path, lineNo, content, isMatch) from one grep/rg
// line. isMatch is true for the ':' separator (a real match) and false for the
// '-' separator (a context line). It returns ok=false for lines that aren't in
// either shape.
//
// The scan skips a leading Windows drive prefix ("C:\" or "C:/") and then
// anchors on the EARLIEST "<sep>\d+<sep>" marker, so paths may contain dashes
// or colons without breaking parsing.
func parseSearchLine(raw string) (path string, lineNo int, content string, isMatch bool, ok bool) {
	if raw == "" {
		return "", 0, "", false, false
	}
	start := 0
	if len(raw) >= 3 && isAlpha(raw[0]) && raw[1] == ':' && (raw[2] == '\\' || raw[2] == '/') {
		start = 2
	}
	for i := start; i < len(raw); i++ {
		sep := raw[i]
		if sep != ':' && sep != '-' {
			continue
		}
		j := i + 1
		for j < len(raw) && raw[j] >= '0' && raw[j] <= '9' {
			j++
		}
		if j == i+1 { // no digits after the separator
			continue
		}
		if j >= len(raw) || raw[j] != sep { // marker not closed by the same separator
			continue
		}
		n, err := strconv.Atoi(raw[i+1 : j])
		if err != nil {
			continue
		}
		p := raw[:i]
		if strings.TrimSpace(p) == "" {
			return "", 0, "", false, false
		}
		return p, n, raw[j+1:], sep == ':', true
	}
	return "", 0, "", false, false
}
