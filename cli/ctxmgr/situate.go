/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package ctxmgr

import (
	"path/filepath"
	"strings"
)

// Situating a passage in its document.
//
// A passage cut from the middle of a file reaches the index saying nothing
// about where it came from: which file, which section, which declaration.
// Retrieval then has to match on the passage's own words alone, so a query
// naming the file or the function it is about ranks that passage no higher
// than any other — the failure the whole hybrid stack was built to avoid.
//
// The fix is a short header prefixed to the text that is indexed: the file
// it belongs to and the nearest enclosing structure above it. It is
// derived from the file itself rather than generated, which keeps indexing
// keyless, offline and free — a corpus of ten thousand passages costs
// nothing to situate, and there is no provider to be down.
//
// What the model reads is unchanged: the header rides on the indexed text
// only, so retrieved passages and their line-range citations stay exactly
// what they were.

// situatedMetaKey tags contexts whose passages carry a situating header,
// so existing corpora keep the vectors they already paid for and only
// contexts created or refreshed after this shipped are indexed with it.
const (
	situatedMetaKey = "situated"
	situatedV1      = "v1"
)

// Situated reports whether this context's passages should be indexed with
// a situating header.
func Situated(fc *FileContext) bool {
	return fc != nil && fc.Metadata != nil && fc.Metadata[situatedMetaKey] == situatedV1
}

// situate fills in the Context header of every passage of a file.
//
// The header names the file and, when the passage does not open with one
// itself, the nearest structural line above it — a markdown heading, a
// declaration. Passages that already begin at a boundary say so by their
// own first line and take the file alone.
func situate(segs []Segment, lines []string) {
	for i := range segs {
		segs[i].Context = situatingHeader(segs[i], lines)
	}
}

// situatingHeader builds one passage's header.
func situatingHeader(s Segment, lines []string) string {
	parts := []string{filepath.Base(s.FilePath)}
	if dir := filepath.Dir(s.FilePath); dir != "." && dir != string(filepath.Separator) && dir != "" {
		parts[0] = s.FilePath
	}
	if enclosing := enclosingStructure(lines, s.StartLine, s.FileType); enclosing != "" {
		parts = append(parts, enclosing)
	}
	return strings.Join(parts, " — ")
}

// enclosingStructure walks up from a passage's first line to the nearest
// line that opens a structure, and returns it trimmed to one readable
// phrase. Empty when the passage already starts on one, or when nothing
// above it qualifies.
func enclosingStructure(lines []string, startLine int, fileType string) string {
	idx := startLine - 1 // StartLine is 1-based
	if idx < 0 || idx >= len(lines) {
		return ""
	}
	if boundaryScore(lines[idx], fileType) >= 3 {
		return "" // the passage opens with its own structure
	}
	for i := idx - 1; i >= 0 && idx-i <= situateLookback; i-- {
		if boundaryScore(lines[i], fileType) >= 3 {
			return trimStructureLine(lines[i])
		}
	}
	return ""
}

// situateLookback bounds how far above a passage the search goes. Far
// enough to find the function or heading it sits in, short enough that a
// passage in a long flat file is not labeled with something unrelated.
const situateLookback = 400

// trimStructureLine reduces a declaration or heading to the part worth
// indexing: no leading markers, no trailing brace, one line, bounded.
func trimStructureLine(line string) string {
	t := strings.TrimSpace(line)
	t = strings.TrimLeft(t, "#")
	t = strings.TrimSpace(t)
	t = strings.TrimSuffix(t, "{")
	t = strings.TrimSpace(t)
	if len(t) > situateHeaderMax {
		cut := t[:situateHeaderMax]
		if i := strings.LastIndex(cut, " "); i > situateHeaderMax/2 {
			cut = cut[:i]
		}
		t = cut
	}
	return t
}

const situateHeaderMax = 120

// IndexText is the text a passage is indexed and embedded by: the
// situating header, when it has one, and then the passage itself. Callers
// that render a passage for the model use Content instead — the header
// exists to be matched against, not to be read back.
func (s Segment) IndexText() string {
	if s.Context == "" {
		return s.Content
	}
	return s.Context + "\n" + s.Content
}
