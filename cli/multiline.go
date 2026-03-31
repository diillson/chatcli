/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"fmt"
	"strings"
)

const multilineDelimiter = "```"

// MultilineBuffer accumulates lines between ``` delimiters, providing
// consistent multiline input support across chat, agent, and coder modes.
//
// Usage:
//
//	buf := &MultilineBuffer{}
//	complete, text := buf.ProcessLine(line)
//	if !complete { /* show continuation prompt */ }
//	else { /* submit text */ }
type MultilineBuffer struct {
	active bool
	lines  []string
}

// Active returns true when the buffer is accumulating multiline input.
func (mb *MultilineBuffer) Active() bool {
	return mb.active
}

// Reset clears the buffer state (e.g., on Ctrl+C).
func (mb *MultilineBuffer) Reset() {
	mb.active = false
	mb.lines = nil
}

// LineCount returns the number of lines accumulated so far.
func (mb *MultilineBuffer) LineCount() int {
	return len(mb.lines)
}

// ProcessLine feeds a new line into the buffer.
//
// Returns:
//   - complete=false: the buffer is accumulating; show a continuation prompt.
//   - complete=true:  fullText contains the final input to submit.
//
// Delimiters: typing ``` on a line by itself toggles multiline mode.
// The opening and closing ``` lines are NOT included in the output.
func (mb *MultilineBuffer) ProcessLine(line string) (complete bool, fullText string) {
	trimmed := strings.TrimSpace(line)

	if !mb.active {
		// Check if this line opens a multiline block
		if trimmed == multilineDelimiter {
			mb.active = true
			mb.lines = nil
			fmt.Printf("  \033[90m📝 Multiline mode — type %s on a new line to finish\033[0m\n", multilineDelimiter)
			return false, ""
		}
		// Normal single-line input
		return true, line
	}

	// In multiline mode — check for closing delimiter
	if trimmed == multilineDelimiter {
		mb.active = false
		result := strings.Join(mb.lines, "\n")
		mb.lines = nil
		return true, result
	}

	mb.lines = append(mb.lines, line)
	return false, ""
}
