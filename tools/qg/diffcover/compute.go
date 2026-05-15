/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package diffcover

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// FileResult is the per-file outcome of patch coverage.
//
// Total counts added lines that fall inside at least one cover block
// (i.e. are "executable" Go statements). Covered counts the subset of
// those that fall in a block with Count > 0.
//
// MissingLines is the sorted list of added executable lines that are NOT
// covered — directly actionable for reviewers.
type FileResult struct {
	Path         string
	Total        int
	Covered      int
	MissingLines []int
}

// Percent returns the per-file coverage percentage, or 100 when there are
// no executable added lines (vacuously covered).
func (f FileResult) Percent() float64 {
	if f.Total == 0 {
		return 100
	}
	return 100 * float64(f.Covered) / float64(f.Total)
}

// Result aggregates patch coverage across the diff.
type Result struct {
	Files     []FileResult
	Total     int
	Covered   int
	Threshold float64
}

// Percent returns the overall patch coverage.
func (r Result) Percent() float64 {
	if r.Total == 0 {
		return 100
	}
	return 100 * float64(r.Covered) / float64(r.Total)
}

// Passed reports whether the overall percent clears the threshold.
func (r Result) Passed() bool {
	return r.Percent()+1e-9 >= r.Threshold
}

// Compute joins a profile (already prefix-stripped to repo-relative paths)
// with a diff to produce patch coverage. Files outside the cover profile
// are treated as "had no measurable executable content" — they contribute
// nothing to Total. If a Go non-test file in the diff has zero matches in
// the profile, that's an explicit signal of uninstrumented coverage (the
// caller's test invocation likely missed `-coverpkg=./...`); we report it
// via the returned set so the wrapper can refuse to pass the gate.
func Compute(p *Profile, d *Diff, includeFile func(path string) bool, threshold float64) (Result, []string) {
	var (
		results    []FileResult
		uninstr    []string
		totalAll   int
		coveredAll int
	)

	paths := make([]string, 0, len(d.Files))
	for path := range d.Files {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	for _, path := range paths {
		if !includeFile(path) {
			continue
		}
		fd := d.Files[path]
		blocks, hasProfile := p.Blocks[path]

		if !hasProfile && len(fd.AddedLines) > 0 {
			uninstr = append(uninstr, path)
			continue
		}

		fr := FileResult{Path: path}
		for line := range fd.AddedLines {
			block, ok := findCovering(blocks, line)
			if !ok {
				continue
			}
			fr.Total++
			if block.Count > 0 {
				fr.Covered++
			} else {
				fr.MissingLines = append(fr.MissingLines, line)
			}
		}
		sort.Ints(fr.MissingLines)

		if fr.Total > 0 || len(fd.AddedLines) > 0 {
			// Skip files where every added line was non-executable (comments,
			// blanks, braces) — they don't move the needle.
			if fr.Total == 0 {
				continue
			}
			results = append(results, fr)
			totalAll += fr.Total
			coveredAll += fr.Covered
		}
	}

	return Result{
		Files:     results,
		Total:     totalAll,
		Covered:   coveredAll,
		Threshold: threshold,
	}, uninstr
}

// findCovering returns the smallest covering block (or the first match,
// they're equivalent for our purposes). Blocks are not pre-sorted because
// the cover profile already emits them in source order, and patch
// coverage only needs *any* covering block.
func findCovering(blocks []CoverBlock, line int) (CoverBlock, bool) {
	for _, b := range blocks {
		if b.Covers(line) {
			return b, true
		}
	}
	return CoverBlock{}, false
}

// MatchAny reports whether path matches at least one of the include patterns.
//
// Glob semantics (kept deliberately small — only what the Quality Gate
// configs use):
//
//   - `*<suffix>` — pure suffix glob ("*.go", "*_test.go"). Matches anywhere
//     in the path tree, unlike filepath.Match which treats `/` as a separator.
//   - `<prefix>/**` — recursive prefix ("proto/**", "tools/docgen/**").
//   - `**/<name>` — anywhere by basename ("**/foo.pb.go").
//   - anything else — passes through to filepath.Match as a fallback.
func MatchAny(path string, patterns []string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		if matchPattern(path, p) {
			return true
		}
	}
	return false
}

// MatchNone reports whether path does NOT match any of the exclude patterns.
// Uses the same semantics as MatchAny.
func MatchNone(path string, patterns []string) bool {
	for _, p := range patterns {
		if matchPattern(path, p) {
			return false
		}
	}
	return true
}

// matchPattern is the shared kernel: explicit handling for the three glob
// shapes we support, plus filepath.Match as a fallback for literal patterns.
func matchPattern(path, pattern string) bool {
	// Suffix glob: "*<suffix>" with no other wildcards. Must match anywhere
	// in the tree, so we compare the path's tail not just its basename.
	if strings.HasPrefix(pattern, "*") &&
		!strings.ContainsAny(pattern[1:], "*/") {
		return strings.HasSuffix(path, pattern[1:])
	}
	// Recursive prefix: "<dir>/**" matches the directory itself or any
	// descendant.
	if strings.HasSuffix(pattern, "/**") {
		prefix := strings.TrimSuffix(pattern, "/**")
		return path == prefix || strings.HasPrefix(path, prefix+"/")
	}
	// Anywhere-by-basename: "**/<name>" matches any path ending in /<name>
	// or equal to <name>.
	if strings.HasPrefix(pattern, "**/") {
		tail := strings.TrimPrefix(pattern, "**/")
		return path == tail || strings.HasSuffix(path, "/"+tail)
	}
	// Fallback to filepath.Match for literal patterns (no globs).
	ok, _ := filepath.Match(pattern, path)
	return ok
}

// FormatMarkdown renders a sticky-comment-friendly markdown table. The
// caller decides whether to include the table at all (e.g. only on
// failure).
func (r Result) FormatMarkdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "**Patch coverage:** %.1f%% (%d/%d executable lines covered)\n\n", r.Percent(), r.Covered, r.Total)
	if len(r.Files) == 0 {
		b.WriteString("_No executable added lines in this diff._\n")
		return b.String()
	}
	b.WriteString("| File | Covered | Total | % |\n|---|---:|---:|---:|\n")
	for _, fr := range r.Files {
		fmt.Fprintf(&b, "| `%s` | %d | %d | %.1f%% |\n", fr.Path, fr.Covered, fr.Total, fr.Percent())
	}
	// Show top missing lines per file for the worst offenders.
	worst := worstFiles(r.Files, 5)
	if len(worst) > 0 {
		b.WriteString("\n<details><summary>Uncovered lines (top 5 files)</summary>\n\n")
		for _, fr := range worst {
			if len(fr.MissingLines) == 0 {
				continue
			}
			fmt.Fprintf(&b, "- `%s`: %s\n", fr.Path, formatLineList(fr.MissingLines))
		}
		b.WriteString("\n</details>\n")
	}
	return b.String()
}

func worstFiles(files []FileResult, n int) []FileResult {
	cp := append([]FileResult(nil), files...)
	sort.SliceStable(cp, func(i, j int) bool {
		return cp[i].Percent() < cp[j].Percent()
	})
	if len(cp) > n {
		cp = cp[:n]
	}
	return cp
}

func formatLineList(lines []int) string {
	// Compact contiguous runs: 1,2,3,5,6,9 -> "1-3, 5-6, 9".
	if len(lines) == 0 {
		return ""
	}
	var parts []string
	start := lines[0]
	prev := lines[0]
	for i := 1; i < len(lines); i++ {
		if lines[i] == prev+1 {
			prev = lines[i]
			continue
		}
		parts = append(parts, formatRange(start, prev))
		start = lines[i]
		prev = lines[i]
	}
	parts = append(parts, formatRange(start, prev))
	return strings.Join(parts, ", ")
}

func formatRange(a, b int) string {
	if a == b {
		return fmt.Sprintf("%d", a)
	}
	return fmt.Sprintf("%d-%d", a, b)
}
