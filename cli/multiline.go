/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"strings"
)

// MultilineBuffer accumulates lines between --- delimiters, providing
// consistent multiline input support across chat, agent, and coder modes.
//
// Accepted delimiter (on a line by itself):
//   - --- (3 or more dashes)
//
// Usage:
//
//	buf := &MultilineBuffer{}
//	complete, text := buf.ProcessLine(line)
//	if !complete { /* show continuation prompt */ }
//	else { /* submit text */ }
type MultilineBuffer struct {
	active    bool
	lines     []string
	delimiter string // the delimiter that opened the block (used for closing)
}

// Active returns true when the buffer is accumulating multiline input.
func (mb *MultilineBuffer) Active() bool {
	return mb.active
}

// Reset clears the buffer state (e.g., on Ctrl+C).
func (mb *MultilineBuffer) Reset() {
	mb.active = false
	mb.lines = nil
	mb.delimiter = ""
}

// LineCount returns the number of lines accumulated so far.
func (mb *MultilineBuffer) LineCount() int {
	return len(mb.lines)
}

// Delimiter returns the delimiter that opened the current multiline block.
func (mb *MultilineBuffer) Delimiter() string {
	return mb.delimiter
}

// isMultilineDelimiter checks if a trimmed line is a multiline delimiter.
// Accepts: --- (3 or more dashes).
func isMultilineDelimiter(trimmed string) string {
	if len(trimmed) >= 3 && strings.Count(trimmed, "-") == len(trimmed) {
		return "---"
	}
	return ""
}

// ProcessLine feeds a new line into the buffer.
//
// Returns:
//   - complete=false: the buffer is accumulating; show a continuation prompt.
//   - complete=true:  fullText contains the final input to submit.
//
// The opening and closing delimiter lines are NOT included in the output.
func (mb *MultilineBuffer) ProcessLine(line string) (complete bool, fullText string) {
	trimmed := strings.TrimSpace(line)

	if !mb.active {
		// Check if this line opens a multiline block
		if delim := isMultilineDelimiter(trimmed); delim != "" {
			mb.active = true
			mb.lines = nil
			mb.delimiter = delim
			return false, ""
		}
		// Normal single-line input
		return true, line
	}

	// In multiline mode — check for closing delimiter (same family as opener)
	if delim := isMultilineDelimiter(trimmed); delim == mb.delimiter {
		mb.active = false
		result := strings.Join(mb.lines, "\n")
		mb.lines = nil
		mb.delimiter = ""
		return true, result
	}

	mb.lines = append(mb.lines, line)
	return false, ""
}
