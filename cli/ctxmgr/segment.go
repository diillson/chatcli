/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Passage segmentation for semantic /context retrieval.
 *
 * The legacy FileChunk groups WHOLE files into ~30k-token buckets — the right
 * grain for "inject everything under a token budget", the wrong grain for
 * retrieval: a 30k-token chunk is itself too large to embed meaningfully or to
 * return as a focused answer. Segment is the retrieval grain: line-aware windows
 * of a few hundred tokens with a small overlap so a match never falls in a seam.
 * Whole files stay verbatim for non-RAG attachments; segments exist only to be
 * embedded and ranked.
 */
package ctxmgr

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/diillson/chatcli/utils"
)

// Segment is one retrievable passage of a file.
type Segment struct {
	ID        string // stable content hash — the vector-index key
	FilePath  string
	FileType  string
	StartLine int // 1-based, inclusive
	EndLine   int // 1-based, inclusive
	Content   string
	// Context situates the passage in its document — the file and the
	// nearest structure enclosing it. It is prefixed to the text the
	// passage is indexed by (see IndexText) and never to what the model
	// reads, so citations and rendered passages are unchanged. Empty on
	// corpora indexed before situating shipped.
	Context string
}

// SegmentOptions tunes how files are split into passages.
type SegmentOptions struct {
	MaxChars     int // soft cap per segment (~4 chars/token); default 1200 ≈ 300 tokens
	OverlapLines int // lines replayed at the start of the next segment; default 2
	// Boundaries makes the cut prefer a structural line (heading, blank
	// line, declaration) inside the last part of the window instead of
	// the exact size limit — passages then start and end on units the
	// file type reads by. Only contexts created with the v2 segmenter
	// (see segmentOptionsFor) opt in, so existing corpora keep their ids.
	Boundaries bool
	// Situate prefixes each passage's indexed text with the file and the
	// structure enclosing it (see situate.go). Only contexts tagged for it
	// opt in, so existing corpora keep the vectors they already paid for.
	Situate bool
}

// segmenterV2 tags contexts created after boundary-aware segmentation
// shipped; older contexts keep the fixed-window cut (and their vectors).
const (
	segmenterMetaKey = "segmenter"
	segmenterV2      = "v2"
)

// currentIndexSeals are the seals every context created — or refreshed —
// from here on carries. They are tags rather than global switches so a
// corpus is only ever re-cut and re-embedded when the context is built or
// refreshed, never on the next query — a live corpus keeps serving the
// vectors it already paid for until a refresh moves it.
func currentIndexSeals() map[string]string {
	return map[string]string{
		segmenterMetaKey: segmenterV2,
		situatedMetaKey:  situatedV1,
	}
}

// applyIndexSeals writes the current seals onto a context and reports
// whether any of them changed — the signal that its passages have to be
// re-cut and its vectors re-earned even when no file moved.
func applyIndexSeals(fc *FileContext) bool {
	if fc == nil {
		return false
	}
	changed := false
	for k, v := range currentIndexSeals() {
		if fc.Metadata == nil {
			fc.Metadata = map[string]string{}
		}
		if fc.Metadata[k] != v {
			fc.Metadata[k] = v
			changed = true
		}
	}
	return changed
}

// indexSeal renders a context's seals as a stable string for the
// retrieval fingerprint. Sorted, so map order never invalidates a cache.
func indexSeal(fc *FileContext) string {
	if fc == nil || fc.Metadata == nil {
		return ""
	}
	keys := make([]string, 0, 2)
	for k := range currentIndexSeals() {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(fc.Metadata[k])
	}
	return b.String()
}

// segmentOptionsFor derives the segmenter options for one context from the
// engine defaults and the context's own tag.
func segmentOptionsFor(fc *FileContext, base SegmentOptions) SegmentOptions {
	base.Situate = Situated(fc)
	if fc != nil && fc.Metadata != nil && fc.Metadata[segmenterMetaKey] == segmenterV2 {
		base.Boundaries = true
		if base.OverlapLines < 3 {
			base.OverlapLines = 3
		}
	}
	return base
}

const (
	defaultSegmentMaxChars     = 1200
	defaultSegmentOverlapLines = 2
)

func (o SegmentOptions) sanitized() SegmentOptions {
	if o.MaxChars <= 0 {
		o.MaxChars = defaultSegmentMaxChars
	}
	if o.OverlapLines < 0 {
		o.OverlapLines = 0
	}
	// Overlap must stay strictly below a typical segment height or windows
	// could fail to advance; cap it defensively.
	if o.OverlapLines > 32 {
		o.OverlapLines = 32
	}
	return o
}

// SegmentFiles splits every file into overlapping, line-aware passages. The
// output order is deterministic (file order, then top-to-bottom), and segment
// ids are content hashes so re-segmenting unchanged files yields identical ids —
// which lets the vector index skip re-embedding work that hasn't changed.
func SegmentFiles(files []utils.FileInfo, opts SegmentOptions) []Segment {
	opts = opts.sanitized()
	var segments []Segment
	for _, f := range files {
		segments = append(segments, segmentOne(f, opts)...)
	}
	return segments
}

func segmentOne(f utils.FileInfo, opts SegmentOptions) []Segment {
	content := f.Content
	if strings.TrimSpace(content) == "" {
		return nil
	}
	// Binary and minified content never reaches the embedder: a NUL byte or
	// invalid UTF-8 is binary; a file that is one or two enormous lines is
	// a bundle nobody can cite a line of. One such passage used to poison
	// the whole provider batch (400) and silently stop the warm-up.
	if reason := unembeddableReason(content); reason != "" {
		return nil
	}
	lines := strings.Split(content, "\n")
	// Hard cap per passage: an over-long line is cut rune-safe at
	// maxPassageBytes so a single line can never exceed a provider's
	// per-input limit.
	capBytes := opts.MaxChars * passageHardCapFactor
	if capBytes <= 0 {
		capBytes = defaultSegmentMaxChars * passageHardCapFactor
	}
	for i, l := range lines {
		if len(l) > capBytes {
			lines[i] = l[:alignRuneBefore(l, capBytes)] + "…"
		}
	}

	var out []Segment
	start := 0 // 0-based index into lines
	for start < len(lines) {
		end := start
		size := 0
		// Grow the window until the next line would overflow MaxChars, but
		// always take at least one line so an over-long line still emits.
		for end < len(lines) {
			lineLen := len(lines[end]) + 1 // +1 for the newline
			if end > start && size+lineLen > opts.MaxChars {
				break
			}
			size += lineLen
			end++
		}
		// A boundary cut ends the window early and the boundary line opens
		// the next segment verbatim — no overlap, or the structure would be
		// replayed inside the previous passage.
		cutAtBoundary := false
		if opts.Boundaries && end < len(lines) {
			if b := boundaryCut(lines, f.Type, start, end); b < end {
				end, cutAtBoundary = b, true
			}
		}

		body := strings.Join(lines[start:end], "\n")
		if strings.TrimSpace(body) != "" {
			out = append(out, Segment{
				ID:        segmentID(f.Path, start+1, body),
				FilePath:  f.Path,
				FileType:  f.Type,
				StartLine: start + 1,
				EndLine:   end,
				Content:   body,
			})
		}

		if end >= len(lines) {
			break
		}
		// Advance with overlap, but never stall: the next start must move past
		// the current one even when overlap >= window height.
		next := end - opts.OverlapLines
		if cutAtBoundary || next <= start {
			next = end
		}
		start = next
	}
	if opts.Situate {
		// Derived after the cut so each passage knows its own first line:
		// the header names the file and the structure enclosing it.
		situate(out, lines)
		// The id keys the vector cache, so it has to name what was
		// embedded, not only what was cut. Boundary-aware segmentation
		// moves the cut and changes the id on its own; situating leaves
		// the cut alone and changes only the indexed text, so without
		// this a corpus that gained a header kept every id it had, the
		// index reported nothing missing, and the header never reached
		// a single vector.
		for i := range out {
			out[i].ID = situatedSegmentID(out[i].ID, out[i].Context)
		}
	}
	return out
}

// passageHardCapFactor × MaxChars is the byte ceiling of one passage line.
const passageHardCapFactor = 4

// minifiedLineBytes is the longest line a human-authored source file has;
// beyond it, with almost no line breaks, the file is a bundle/minified
// artifact. minifiedMinBytes keeps small one-liners (a long JSON line, a
// data URI) on the normal path: they are capped, not dropped.
const (
	minifiedLineBytes = 4096
	minifiedMinBytes  = 32 * 1024
)

// unembeddableReason classifies content the ingest must skip: "binary"
// (NUL byte or invalid UTF-8) or "minified" (huge lines, no structure);
// "" when the content is ordinary text.
func unembeddableReason(content string) string {
	if strings.IndexByte(content, 0) >= 0 || !utf8.ValidString(content) {
		return "binary"
	}
	if len(content) < minifiedMinBytes {
		return ""
	}
	longest, lines := 0, 1
	start := 0
	for i := 0; i <= len(content); i++ {
		if i == len(content) || content[i] == '\n' {
			if l := i - start; l > longest {
				longest = l
			}
			if i < len(content) {
				lines++
			}
			start = i + 1
		}
	}
	if longest >= minifiedLineBytes && len(content)/lines >= minifiedLineBytes/4 {
		return "minified"
	}
	return ""
}

// segmentID is a stable, collision-resistant key for the vector index. It folds
// in the path and start line so identical text in two files (or two places) maps
// to distinct vectors, while unchanged content keeps the same id across rebuilds.
func segmentID(path string, startLine int, body string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(path))
	_, _ = h.Write([]byte{0})
	// start line as bytes for cheap disambiguation without fmt
	_, _ = h.Write([]byte(itoa(startLine)))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(body))
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:12])
}

// situatedSegmentID derives the id of a passage indexed with a situating
// header from the id of its raw cut. Passages without a header keep the
// bare id, so turning situating on is the only thing that moves them.
func situatedSegmentID(base, context string) string {
	if context == "" {
		return base
	}
	h := sha256.New()
	_, _ = h.Write([]byte(base))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(context))
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:12])
}

// itoa is a tiny allocation-light integer formatter for the hash input.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// boundaryCut pulls a window end back to the best structural boundary in
// its last 40% (never below start+1). Boundary strength by file type:
// markdown headings and code declarations outrank blank lines, which
// outrank closing braces; among equals the latest line wins so windows
// stay as full as the structure allows. The returned end is exclusive
// and the boundary line opens the next segment.
func boundaryCut(lines []string, fileType string, start, end int) int {
	span := end - start
	if span < 4 {
		return end
	}
	lookback := span * 2 / 5
	if lookback < 1 {
		lookback = 1
	}
	best, bestScore := end, 0
	for i := end - 1; i > end-1-lookback && i > start; i-- {
		if s := boundaryScore(lines[i], fileType); s > bestScore {
			best, bestScore = i, s
		}
	}
	return best
}

func boundaryScore(line, fileType string) int {
	t := strings.TrimSpace(line)
	if t == "" {
		return 2
	}
	switch strings.ToLower(strings.TrimPrefix(fileType, ".")) {
	case "md", "markdown", "mdx", "txt", "rst", "adoc":
		if strings.HasPrefix(t, "#") {
			return 3
		}
		return 0
	}
	for _, kw := range []string{"func ", "def ", "class ", "type ", "impl ", "fn ", "interface ", "struct ", "public ", "private ", "protected ", "static ", "export ", "module ", "package "} {
		if strings.HasPrefix(t, kw) {
			return 3
		}
	}
	if t == "}" || t == "};" || t == "end" {
		return 1
	}
	return 0
}
