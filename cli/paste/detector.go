/*
 * ChatCLI - Paste Detection
 * cli/paste/detector.go
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package paste

import (
	"bytes"
	"os"
	"runtime"
	"strings"
	"sync"

	prompt "github.com/c-bata/go-prompt"
)

var (
	pasteStartSeq = []byte{0x1b, '[', '2', '0', '0', '~'} // ESC[200~
	pasteEndSeq   = []byte{0x1b, '[', '2', '0', '1', '~'} // ESC[201~
	enableBP      = "\x1b[?2004h"
	disableBP     = "\x1b[?2004l"
)

// Info holds metadata about a detected paste operation.
type Info struct {
	Content   string
	CharCount int
	LineCount int
}

// BracketedPasteParser wraps a ConsoleParser to detect bracketed paste sequences.
// It implements go-prompt's ConsoleParser interface.
type BracketedPasteParser struct {
	inner   prompt.ConsoleParser
	onPaste func(Info)

	mu       sync.Mutex
	pasting  bool
	pasteBuf bytes.Buffer
	pending  []byte // data remaining after processing
	enabled  bool   // whether bracketed paste was actually enabled
}

// NewBracketedPasteParser creates a new parser that wraps an existing ConsoleParser.
// The onPaste callback is invoked when a paste operation is detected.
func NewBracketedPasteParser(inner prompt.ConsoleParser, onPaste func(Info)) *BracketedPasteParser {
	return &BracketedPasteParser{
		inner:   inner,
		onPaste: onPaste,
	}
}

// Setup initializes the parser and enables bracketed paste mode.
func (p *BracketedPasteParser) Setup() error {
	if err := p.inner.Setup(); err != nil {
		return err
	}

	// Skip bracketed paste on old Windows terminals (cmd.exe, PowerShell without WT)
	if runtime.GOOS == "windows" && os.Getenv("WT_SESSION") == "" {
		p.enabled = false
		return nil
	}

	p.enabled = true
	_, _ = os.Stdout.WriteString(enableBP)
	return nil
}

// TearDown disables bracketed paste mode and cleans up.
func (p *BracketedPasteParser) TearDown() error {
	if p.enabled {
		_, _ = os.Stdout.WriteString(disableBP)
	}
	return p.inner.TearDown()
}

// GetWinSize returns the terminal window size.
func (p *BracketedPasteParser) GetWinSize() *prompt.WinSize {
	return p.inner.GetWinSize()
}

// Read reads bytes from the inner parser and processes bracketed paste sequences.
func (p *BracketedPasteParser) Read() ([]byte, error) {
	p.mu.Lock()

	// If we have pending data from a previous read, return it first
	if len(p.pending) > 0 {
		data := p.pending
		p.pending = nil
		p.mu.Unlock()
		return data, nil
	}
	p.mu.Unlock()

	// Read from the inner parser
	data, err := p.inner.Read()
	if err != nil {
		return data, err
	}

	if !p.enabled {
		return data, nil
	}

	return p.processData(data), nil
}

// processData handles bracketed paste detection in the raw byte stream.
func (p *BracketedPasteParser) processData(data []byte) []byte {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.pasting {
		// We're inside a paste — look for the end sequence
		endIdx := bytes.Index(data, pasteEndSeq)
		if endIdx >= 0 {
			// Found end of paste
			p.pasteBuf.Write(data[:endIdx])
			p.pasting = false

			// Extract the full pasted content
			content := p.pasteBuf.String()
			p.pasteBuf.Reset()

			// Notify about the paste
			if p.onPaste != nil {
				charCount := len([]rune(content))
				lineCount := strings.Count(content, "\n") + 1
				p.onPaste(Info{
					Content:   content,
					CharCount: charCount,
					LineCount: lineCount,
				})
			}

			// Remaining data after the end sequence goes to pending
			after := data[endIdx+len(pasteEndSeq):]
			if len(after) > 0 {
				p.pending = append(p.pending, after...)
			}

			// Return the pasted content as regular input for go-prompt
			return []byte(content)
		}

		// No end sequence yet — buffer everything
		p.pasteBuf.Write(data)
		// Return empty to suppress display while pasting
		return nil
	}

	// Not pasting — look for the start sequence
	startIdx := bytes.Index(data, pasteStartSeq)
	if startIdx >= 0 {
		p.pasting = true
		p.pasteBuf.Reset()

		// Data before the start sequence is normal input
		before := data[:startIdx]

		// Data after the start sequence is pasted content
		afterStart := data[startIdx+len(pasteStartSeq):]

		// Check if the end sequence is also in this chunk
		endIdx := bytes.Index(afterStart, pasteEndSeq)
		if endIdx >= 0 {
			// Complete paste in a single read
			content := string(afterStart[:endIdx])
			p.pasting = false

			if p.onPaste != nil {
				charCount := len([]rune(content))
				lineCount := strings.Count(content, "\n") + 1
				p.onPaste(Info{
					Content:   content,
					CharCount: charCount,
					LineCount: lineCount,
				})
			}

			// Data after end sequence
			afterEnd := afterStart[endIdx+len(pasteEndSeq):]
			if len(afterEnd) > 0 {
				p.pending = append(p.pending, afterEnd...)
			}

			// Return before + pasted content
			result := make([]byte, 0, len(before)+len(content))
			result = append(result, before...)
			result = append(result, []byte(content)...)
			return result
		}

		// Start but no end yet — buffer and return only the "before" part
		p.pasteBuf.Write(afterStart)
		if len(before) > 0 {
			return before
		}
		return nil
	}

	// No paste sequences — pass through unchanged
	return data
}

// DetectInLine strips bracketed paste markers from a line read via bufio
// and returns paste info if markers were found.
// This is used for agent mode where we read directly from stdin instead of go-prompt.
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
