/*
 * ChatCLI - Paste Detection
 * cli/paste/detector.go
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package paste

import (
	"strings"
)

var (
	pasteStartSeq = []byte{0x1b, '[', '2', '0', '0', '~'} // ESC[200~
	pasteEndSeq   = []byte{0x1b, '[', '2', '0', '1', '~'} // ESC[201~
)

// Info holds metadata about a detected paste operation.
type Info struct {
	Content   string
	CharCount int
	LineCount int
}

// DetectInLine strips bracketed paste markers from a line read via bufio
// and returns paste info if markers were found.
// This is used for agent mode where we read directly from stdin.
func DetectInLine(line string) (string, *Info) {
	startStr := string(pasteStartSeq)
	endStr := string(pasteEndSeq)

	if !strings.Contains(line, startStr) {
		return line, nil
	}

	cleaned := strings.ReplaceAll(line, startStr, "")
	cleaned = strings.ReplaceAll(cleaned, endStr, "")

	trimmed := strings.TrimSpace(cleaned)
	charCount := len([]rune(trimmed))
	lineCount := strings.Count(cleaned, "\n") + 1

	return cleaned, &Info{
		Content:   trimmed,
		CharCount: charCount,
		LineCount: lineCount,
	}
}
