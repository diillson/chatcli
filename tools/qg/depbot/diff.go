/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package depbot

import "strings"

// hunk holds the changed lines of one diff hunk, in file order and without
// the leading +/- marker. Context lines are dropped: the policy only
// judges what a pull request changes.
type hunk struct {
	removed []string
	added   []string
}

// fileChange is one file of a unified diff.
type fileChange struct {
	path  string
	hunks []hunk
	// shape is non-empty when the file was not simply modified in place
	// (created, deleted, renamed, mode change, binary). A dependency bump
	// never does any of those.
	shape string
}

func (f *fileChange) removed() []string {
	var out []string
	for _, h := range f.hunks {
		out = append(out, h.removed...)
	}
	return out
}

func (f *fileChange) added() []string {
	var out []string
	for _, h := range f.hunks {
		out = append(out, h.added...)
	}
	return out
}

// shapeMarkers maps extended-header prefixes to the shape they denote.
var shapeMarkers = []struct{ prefix, shape string }{
	{"new file mode", "created"},
	{"deleted file mode", "deleted"},
	{"rename from", "renamed"},
	{"copy from", "copied"},
	{"old mode", "mode changed"},
	{"Binary files", "binary"},
	{"GIT binary patch", "binary"},
}

// parseDiff splits a unified diff (git or `gh pr diff` output) into files.
// Inside a hunk every line carries a one-character marker, so a header
// keyword can only be recognized outside one — that is what inHunk tracks.
func parseDiff(diff string) []*fileChange {
	var files []*fileChange
	var cur *fileChange
	inHunk := false
	for _, line := range strings.Split(diff, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.HasPrefix(line, "diff --git ") {
			cur = &fileChange{path: pathFromHeader(line)}
			files = append(files, cur)
			inHunk = false
			continue
		}
		if cur == nil {
			continue
		}
		if strings.HasPrefix(line, "@@") {
			cur.hunks = append(cur.hunks, hunk{})
			inHunk = true
			continue
		}
		if !inHunk {
			for _, m := range shapeMarkers {
				if strings.HasPrefix(line, m.prefix) {
					cur.shape = m.shape
				}
			}
			continue
		}
		h := &cur.hunks[len(cur.hunks)-1]
		switch {
		case strings.HasPrefix(line, "+"):
			h.added = append(h.added, line[1:])
		case strings.HasPrefix(line, "-"):
			h.removed = append(h.removed, line[1:])
		}
	}
	return files
}

// pathFromHeader extracts the new-side path from `diff --git a/x b/x`.
// Paths with spaces are quoted by git and never match the allowlist, so
// they are returned verbatim and rejected downstream.
func pathFromHeader(line string) string {
	rest := strings.TrimPrefix(line, "diff --git ")
	if i := strings.LastIndex(rest, " b/"); i >= 0 {
		return rest[i+3:]
	}
	return rest
}
