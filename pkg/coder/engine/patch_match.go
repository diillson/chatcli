/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * patch_match.go — how a search/replace edit finds its target.
 *
 * An edit the model asks for must land exactly where it meant: a search
 * text that occurs twice is refused with the lines it matched (replace the
 * first silently and the model edits the wrong place without knowing), a
 * search text that occurs nowhere gets the closest line of the file so the
 * next attempt copies the real text, and a search text that differs only
 * in indentation or trailing whitespace is accepted when it matches one
 * place, with the replacement re-indented to the file.
 */
package engine

import (
	"fmt"
	"sort"
	"strings"

	"github.com/diillson/chatcli/pkg/textfold"
)

// patchMatch is where a search text was found in a file.
type patchMatch struct {
	start, end int  // byte span in the content
	line       int  // 1-based line of start
	tolerant   bool // matched ignoring indentation/trailing whitespace
}

// errPatchAmbiguous and errPatchNotFound are the two refusals; both carry
// the way forward in their text.
const (
	patchAmbiguousFmt = "search text occurs %d times in %s (lines %s): include more surrounding lines so it matches one place, or pass --all to replace every occurrence"
	patchNotFoundFmt  = "search text not found in %s%s. Read the file around that line and copy the text exactly as it is there (whitespace and line breaks included), then patch again"
	patchClosestFmt   = "; closest line is %d: %q"
)

// locatePatch finds search in content. Exact occurrences win; with all set
// every exact occurrence is returned, otherwise exactly one must exist.
// Without an exact occurrence a whitespace-tolerant match is tried and
// accepted when unique. The returned error says what to do next.
func locatePatch(content, search, file string, all bool) ([]patchMatch, error) {
	if search == "" {
		return nil, fmt.Errorf("search text is empty")
	}
	exact := exactMatches(content, search)
	switch {
	case len(exact) == 1:
		return exact, nil
	case len(exact) > 1 && all:
		return exact, nil
	case len(exact) > 1:
		return nil, fmt.Errorf(patchAmbiguousFmt, len(exact), file, matchLines(exact))
	}
	if tol := tolerantMatches(content, search); len(tol) == 1 {
		return tol, nil
	} else if len(tol) > 1 && all {
		return tol, nil
	} else if len(tol) > 1 {
		return nil, fmt.Errorf(patchAmbiguousFmt, len(tol), file, matchLines(tol))
	}
	hint := ""
	if line, text, ok := closestLine(content, search); ok {
		hint = fmt.Sprintf(patchClosestFmt, line, text)
	}
	return nil, fmt.Errorf(patchNotFoundFmt, file, hint)
}

// applyPatches replaces every match (non-overlapping, in order) with
// replace, re-indenting a tolerant match's replacement to the file.
func applyPatches(content, search, replace string, matches []patchMatch) string {
	if len(matches) == 0 {
		return content
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].start < matches[j].start })
	var b strings.Builder
	pos := 0
	for _, m := range matches {
		if m.start < pos {
			continue // overlapping (cannot happen for distinct matches, kept for safety)
		}
		b.WriteString(content[pos:m.start])
		rep := replace
		if m.tolerant {
			rep = reindent(content[m.start:m.end], search, replace)
		}
		b.WriteString(rep)
		pos = m.end
	}
	b.WriteString(content[pos:])
	return b.String()
}

// exactMatches returns every non-overlapping exact occurrence.
func exactMatches(content, search string) []patchMatch {
	var out []patchMatch
	pos := 0
	for {
		i := strings.Index(content[pos:], search)
		if i < 0 {
			return out
		}
		start := pos + i
		out = append(out, patchMatch{start: start, end: start + len(search), line: lineOf(content, start)})
		pos = start + len(search)
		if pos >= len(content) {
			return out
		}
	}
}

// tolerantMatches finds search ignoring leading indentation and trailing
// whitespace on every line: the lines of search, trimmed, must equal a run
// of trimmed content lines.
func tolerantMatches(content, search string) []patchMatch {
	want := trimmedLines(strings.TrimRight(search, "\n"))
	if len(want) == 0 {
		return nil
	}
	lines := strings.SplitAfter(content, "\n")
	offsets := make([]int, len(lines)+1)
	for i, l := range lines {
		offsets[i+1] = offsets[i] + len(l)
	}
	var out []patchMatch
	for i := 0; i+len(want) <= len(lines); i++ {
		ok := true
		for j := range want {
			if strings.TrimSpace(lines[i+j]) != want[j] {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		end := offsets[i+len(want)]
		// Keep the newline after the last matched line out of the span
		// unless the search text itself ended with one.
		if !strings.HasSuffix(search, "\n") && end > 0 && content[end-1] == '\n' {
			end--
		}
		out = append(out, patchMatch{start: offsets[i], end: end, line: i + 1, tolerant: true})
		i += len(want) - 1
	}
	return out
}

// trimmedLines splits s into lines with surrounding whitespace removed.
func trimmedLines(s string) []string {
	raw := strings.Split(s, "\n")
	out := make([]string, len(raw))
	for i, l := range raw {
		out[i] = strings.TrimSpace(l)
	}
	return out
}

// reindent returns replace with the indentation difference between the
// file's first matched line and the search's first line applied to every
// non-empty replacement line, so a block matched one level deeper lands
// one level deeper.
func reindent(actual, search, replace string) string {
	actualIndent := leadingWhitespace(firstNonBlank(actual))
	searchIndent := leadingWhitespace(firstNonBlank(search))
	if actualIndent == searchIndent {
		return replace
	}
	lines := strings.Split(replace, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		switch {
		case strings.HasPrefix(actualIndent, searchIndent):
			lines[i] = actualIndent[len(searchIndent):] + l
		case strings.HasPrefix(searchIndent, actualIndent) && strings.HasPrefix(l, searchIndent[len(actualIndent):]):
			lines[i] = l[len(searchIndent)-len(actualIndent):]
		default:
			lines[i] = actualIndent + strings.TrimLeft(l, " \t")
		}
	}
	return strings.Join(lines, "\n")
}

// firstNonBlank returns the first line of s that has content.
func firstNonBlank(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			return l
		}
	}
	return ""
}

// leadingWhitespace returns the indentation of l.
func leadingWhitespace(l string) string {
	return l[:len(l)-len(strings.TrimLeft(l, " \t"))]
}

// lineOf returns the 1-based line number of byte offset in content.
func lineOf(content string, offset int) int {
	if offset > len(content) {
		offset = len(content)
	}
	return strings.Count(content[:offset], "\n") + 1
}

// matchLines renders the line numbers of matches (at most eight).
func matchLines(matches []patchMatch) string {
	parts := make([]string, 0, len(matches))
	for i, m := range matches {
		if i == 8 {
			parts = append(parts, "…")
			break
		}
		parts = append(parts, fmt.Sprint(m.line))
	}
	return strings.Join(parts, ", ")
}

// closestLine finds the content line most similar to the first non-blank
// line of search: the one sharing the most words with it, ties to the
// earlier line. It is a hint, so a weak best match (no word in common) is
// not reported.
func closestLine(content, search string) (line int, text string, ok bool) {
	target := strings.Fields(strings.TrimSpace(firstNonBlank(search)))
	if len(target) == 0 {
		return 0, "", false
	}
	want := make(map[string]bool, len(target))
	for _, w := range target {
		want[w] = true
	}
	best, bestScore := -1, 0
	lines := strings.Split(content, "\n")
	for i, l := range lines {
		score := 0
		for _, w := range strings.Fields(l) {
			if want[w] {
				score++
			}
		}
		if score > bestScore {
			best, bestScore = i, score
		}
	}
	if best < 0 {
		return 0, "", false
	}
	text = strings.TrimSpace(lines[best])
	if len(text) > 100 {
		text = textfold.Cut(text, 100) + "…"
	}
	return best + 1, text, true
}
