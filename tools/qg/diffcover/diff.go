package diffcover

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// FileDiff captures the lines added or modified in a single file. Only the
// new-file line numbers matter for patch coverage — context lines are not
// "the diff" and deleted lines have no new-file presence.
type FileDiff struct {
	Path       string
	AddedLines map[int]struct{}
}

// Diff is the parsed result of `git diff --unified=0`.
type Diff struct {
	Files map[string]*FileDiff
}

// ParseUnifiedDiff parses the output of `git diff --unified=0` and returns
// per-file sets of added new-file line numbers. The implementation is a
// small state machine that tracks the current file (set by `diff --git`
// headers) and the current line counter inside each hunk header.
//
// Why hand-rolled instead of a library: we only need new-file line numbers
// from added (`+`) lines, the format is stable, and avoiding deps keeps
// tools/qg's go.sum unaffected.
func ParseUnifiedDiff(r io.Reader) (*Diff, error) {
	d := &Diff{Files: map[string]*FileDiff{}}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	var currentFile *FileDiff
	var newLine int
	var inHunk bool

	for sc.Scan() {
		line := sc.Text()

		switch {
		case strings.HasPrefix(line, "diff --git "):
			path, err := parseDiffGitHeader(line)
			if err != nil {
				return nil, fmt.Errorf("diffcover: %w", err)
			}
			currentFile = d.fileFor(path)
			inHunk = false

		case strings.HasPrefix(line, "rename to "):
			// `git diff --diff-filter=AM` shouldn't include renames, but if
			// the caller forgets the filter, treat the destination path as
			// the file under consideration. Rename source is already gone.
			path := strings.TrimPrefix(line, "rename to ")
			currentFile = d.fileFor(strings.TrimSpace(path))
			inHunk = false

		case strings.HasPrefix(line, "@@ "):
			// "@@ -oldStart[,oldCount] +newStart[,newCount] @@ optional context"
			start, count, err := parseHunkNew(line)
			if err != nil {
				return nil, fmt.Errorf("diffcover: %w", err)
			}
			if count == 0 {
				// Pure deletion hunk — no new-file lines to attribute.
				inHunk = false
				continue
			}
			newLine = start
			inHunk = true

		case inHunk && strings.HasPrefix(line, "+++"):
			// `+++ b/path` header — not a content line.
			continue

		case inHunk && strings.HasPrefix(line, "+"):
			if currentFile != nil {
				currentFile.AddedLines[newLine] = struct{}{}
			}
			newLine++

		case inHunk && strings.HasPrefix(line, "-"):
			// Removed line — does not advance new-file counter.

		case inHunk && (strings.HasPrefix(line, " ") || line == ""):
			// Context line under --unified=0 only appears if the user
			// passed a higher context; advance to keep numbers aligned.
			newLine++
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("diffcover: scan diff: %w", err)
	}
	return d, nil
}

func (d *Diff) fileFor(path string) *FileDiff {
	if f, ok := d.Files[path]; ok {
		return f
	}
	f := &FileDiff{Path: path, AddedLines: map[int]struct{}{}}
	d.Files[path] = f
	return f
}

func parseDiffGitHeader(line string) (string, error) {
	// "diff --git a/<path> b/<path>"
	parts := strings.SplitN(strings.TrimPrefix(line, "diff --git "), " ", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("diff header malformed: %q", line)
	}
	bPath := strings.TrimSpace(parts[1])
	if !strings.HasPrefix(bPath, "b/") {
		return "", fmt.Errorf("diff header b-path malformed: %q", line)
	}
	return strings.TrimPrefix(bPath, "b/"), nil
}

// parseHunkNew extracts (newStart, newCount) from a hunk header.
// `-oldStart[,oldCount]` is ignored; count defaults to 1 if omitted.
func parseHunkNew(line string) (int, int, error) {
	// Find the "+...." segment.
	plus := strings.Index(line, "+")
	if plus < 0 {
		return 0, 0, fmt.Errorf("hunk header has no +section: %q", line)
	}
	rest := line[plus+1:]
	end := strings.Index(rest, " ")
	if end < 0 {
		return 0, 0, fmt.Errorf("hunk header truncated: %q", line)
	}
	spec := rest[:end]

	start := spec
	count := "1"
	if comma := strings.IndexByte(spec, ','); comma >= 0 {
		start = spec[:comma]
		count = spec[comma+1:]
	}
	s, err := strconv.Atoi(start)
	if err != nil {
		return 0, 0, fmt.Errorf("hunk start not int: %q", line)
	}
	c, err := strconv.Atoi(count)
	if err != nil {
		return 0, 0, fmt.Errorf("hunk count not int: %q", line)
	}
	return s, c, nil
}
