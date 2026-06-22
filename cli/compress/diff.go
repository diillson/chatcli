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

// DiffCompressor reduces unified-diff output (git diff / git show). The
// information that matters is the changed lines (+/-) and the hunk headers;
// long runs of unchanged context lines are noise the model rarely needs. This
// compressor keeps every addition and deletion, trims context to a small
// window around each change, and caps hunks-per-file and files. Dropped
// context is offloaded to CCR.
type DiffCompressor struct {
	MaxContextLines int
	MaxHunksPerFile int
	MaxFiles        int
}

// NewDiffCompressor returns a compressor with Headroom-equivalent caps.
func NewDiffCompressor() *DiffCompressor {
	return &DiffCompressor{
		MaxContextLines: 2,
		MaxHunksPerFile: 10,
		MaxFiles:        20,
	}
}

// Name implements Compressor.
func (*DiffCompressor) Name() string { return "diff" }

// Detect implements Compressor.
func (c *DiffCompressor) Detect(content string, h Hint) float64 {
	switch strings.ToLower(strings.TrimSpace(h.ToolName)) {
	case "git diff", "git show", "diff":
		return 0.95
	}
	lines := splitLines(content)
	if len(lines) < 5 {
		return 0
	}
	hunks, fileHdrs := 0, 0
	for _, ln := range lines {
		switch {
		case strings.HasPrefix(ln, "@@ "):
			hunks++
		case strings.HasPrefix(ln, "diff --git ") || strings.HasPrefix(ln, "+++ ") || strings.HasPrefix(ln, "--- "):
			fileHdrs++
		}
	}
	if hunks == 0 {
		return 0
	}
	if fileHdrs > 0 && hunks > 0 {
		return 0.9
	}
	return 0.6
}

// Compress implements Compressor.
func (c *DiffCompressor) Compress(content string, opts Options) (Result, error) {
	if !canDrop(opts) {
		// Context trimming is inherently lossy; with no CCR store we leave the
		// diff untouched to honor the never-degrade contract.
		return passthrough(content), nil
	}
	lines := splitLines(content)
	kept, dropped := c.selectDiff(lines)
	if dropped == 0 {
		return passthrough(content), nil
	}

	marker, key := offload(content, opts)
	if marker == "" {
		return passthrough(content), nil
	}

	var b strings.Builder
	gap := 0
	flush := func() {
		if gap > 0 {
			fmt.Fprintf(&b, "... [%d context lines omitted] ...\n", gap)
			gap = 0
		}
	}
	for i, ln := range lines {
		if _, ok := kept[i]; ok {
			flush()
			b.WriteString(ln)
			b.WriteByte('\n')
		} else {
			gap++
		}
	}
	flush()
	fmt.Fprintf(&b, "\n[diff: full context recoverable with @recall %s]\n", marker)

	out := b.String()
	return Result{
		Compressed:     out,
		OriginalSize:   len(content),
		CompressedSize: len(out),
		Strategy:       c.Name(),
		CacheKey:       key,
		Reversible:     true,
		Detail:         map[string]int{"lines_kept": len(kept), "context_dropped": dropped},
	}, nil
}

// selectDiff returns the indices to keep and the count of dropped context
// lines. Every header and every +/- line is kept; context lines are kept only
// within MaxContextLines of a change. Hunks beyond MaxHunksPerFile per file
// and files beyond MaxFiles are reduced to their headers.
func (c *DiffCompressor) selectDiff(lines []string) (map[int]struct{}, int) {
	keep := make(map[int]struct{})

	// First pass: always keep structural headers and change lines; mark
	// context lines as candidates.
	isChange := make([]bool, len(lines))
	fileIdx, hunkInFile := -1, 0
	activeHunkLive := false
	for i, ln := range lines {
		switch {
		case strings.HasPrefix(ln, "diff --git "):
			fileIdx++
			hunkInFile = 0
			activeHunkLive = fileIdx < c.MaxFiles
			keep[i] = struct{}{}
		case strings.HasPrefix(ln, "--- ") || strings.HasPrefix(ln, "+++ ") ||
			strings.HasPrefix(ln, "index ") || strings.HasPrefix(ln, "new file") ||
			strings.HasPrefix(ln, "deleted file") || strings.HasPrefix(ln, "rename "):
			keep[i] = struct{}{}
		case strings.HasPrefix(ln, "@@ "):
			hunkInFile++
			activeHunkLive = fileIdx < c.MaxFiles && hunkInFile <= c.MaxHunksPerFile
			keep[i] = struct{}{} // hunk header always kept
		case strings.HasPrefix(ln, "+") || strings.HasPrefix(ln, "-"):
			if activeHunkLive {
				keep[i] = struct{}{}
				isChange[i] = true
			}
		}
	}

	// Second pass: keep context lines within MaxContextLines of a change.
	dropped := 0
	for i, ln := range lines {
		if _, already := keep[i]; already {
			continue
		}
		isContext := strings.HasPrefix(ln, " ") || ln == ""
		if !isContext {
			continue // non-context, non-kept (e.g. lines in dropped hunks)
		}
		near := false
		lo := i - c.MaxContextLines
		hi := i + c.MaxContextLines
		for j := lo; j <= hi; j++ {
			if j >= 0 && j < len(lines) && isChange[j] {
				near = true
				break
			}
		}
		if near {
			keep[i] = struct{}{}
		} else {
			dropped++
		}
	}
	return keep, dropped
}
