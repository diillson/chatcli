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
	"strings"

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
}

// SegmentOptions tunes how files are split into passages.
type SegmentOptions struct {
	MaxChars     int // soft cap per segment (~4 chars/token); default 1200 ≈ 300 tokens
	OverlapLines int // lines replayed at the start of the next segment; default 2
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
	lines := strings.Split(content, "\n")

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
		if next <= start {
			next = end
		}
		start = next
	}
	return out
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
