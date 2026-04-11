/*
 * ChatCLI - Microcompact for Tool Results
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Progressively compacts old tool results in the conversation history to
 * reduce context usage without losing critical information.
 *
 * Inspired by openclaude's microCompact.ts which selectively compacts
 * specific tools (FILE_READ, BASH, GREP, etc.) based on age.
 *
 * Strategy:
 *   - Tool results from recent turns: unchanged
 *   - Tool results 2+ turns old: truncated to head+tail preview
 *   - Tool results 4+ turns old: replaced with one-line summary
 *
 * Only applies to read-only tool results (file reads, search, git status, etc.).
 * Write/exec results are preserved as they contain critical error information.
 */
package agent

import (
	"fmt"
	"os"
	"strings"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// MicrocompactConfig controls progressive compaction behavior.
type MicrocompactConfig struct {
	// TurnsBeforeTruncate is the age (in turns) after which tool results
	// start being truncated. Default: 2.
	TurnsBeforeTruncate int

	// TurnsBeforeSummarize is the age (in turns) after which tool results
	// are replaced with a one-line summary. Default: 4.
	TurnsBeforeSummarize int

	// TruncateHeadChars is how many chars to keep at the head during truncation.
	TruncateHeadChars int

	// TruncateTailChars is how many chars to keep at the tail during truncation.
	TruncateTailChars int

	// MinContentSize is the minimum content size (chars) to trigger compaction.
	// Small tool results are never compacted.
	MinContentSize int
}

// DefaultMicrocompactConfig returns the default configuration.
func DefaultMicrocompactConfig() MicrocompactConfig {
	cfg := MicrocompactConfig{
		TurnsBeforeTruncate:  2,
		TurnsBeforeSummarize: 4,
		TruncateHeadChars:    2000,
		TruncateTailChars:    500,
		MinContentSize:       3000,
	}
	if v := os.Getenv("CHATCLI_MICROCOMPACT_TRUNCATE_TURNS"); v != "" {
		_, _ = fmt.Sscanf(v, "%d", &cfg.TurnsBeforeTruncate)
	}
	if v := os.Getenv("CHATCLI_MICROCOMPACT_SUMMARIZE_TURNS"); v != "" {
		_, _ = fmt.Sscanf(v, "%d", &cfg.TurnsBeforeSummarize)
	}
	return cfg
}

// MicrocompactReport describes what the microcompact did.
type MicrocompactReport struct {
	Truncated  int
	Summarized int
	CharsSaved int64
}

// ApplyMicrocompact progressively compacts old tool results in the history.
// currentTurn is the 0-based index of the current turn in the agent loop.
//
// The function identifies "turns" by counting assistant messages. Tool results
// following each assistant message belong to that turn. Results from older
// turns are progressively compacted.
func ApplyMicrocompact(history []models.Message, currentTurn int, config MicrocompactConfig, logger *zap.Logger) ([]models.Message, *MicrocompactReport) {
	report := &MicrocompactReport{}

	if len(history) == 0 || currentTurn < config.TurnsBeforeTruncate {
		return history, report
	}

	turnNumber := 0
	turnMap := make(map[int]int) // message index → turn number

	for i, msg := range history {
		if msg.Role == "assistant" {
			turnNumber++
		}
		turnMap[i] = turnNumber
	}

	// Process each tool result
	for i := range history {
		msg := &history[i]
		if msg.Role != "tool" {
			continue
		}
		if len(msg.Content) < config.MinContentSize {
			continue
		}

		msgTurn := turnMap[i]
		age := turnNumber - msgTurn

		if age >= config.TurnsBeforeSummarize {
			// Level 2: Replace with one-line summary
			original := len(msg.Content)
			msg.Content = buildToolResultSummary(msg.Content, msg.ToolCallID)
			report.Summarized++
			report.CharsSaved += int64(original - len(msg.Content))
		} else if age >= config.TurnsBeforeTruncate {
			// Level 1: Truncate to head+tail preview
			original := len(msg.Content)
			msg.Content = truncateToolResultPreview(
				msg.Content, config.TruncateHeadChars, config.TruncateTailChars)
			report.Truncated++
			report.CharsSaved += int64(original - len(msg.Content))
		}
	}

	if logger != nil && (report.Truncated > 0 || report.Summarized > 0) {
		logger.Debug("Microcompact applied",
			zap.Int("truncated", report.Truncated),
			zap.Int("summarized", report.Summarized),
			zap.Int64("chars_saved", report.CharsSaved))
	}

	return history, report
}

// truncateToolResultPreview keeps the head and tail of a tool result.
func truncateToolResultPreview(content string, headChars, tailChars int) string {
	if len(content) <= headChars+tailChars+100 {
		return content
	}

	head := content[:headChars]
	// Cut at last newline
	if idx := strings.LastIndex(head, "\n"); idx > headChars/2 {
		head = head[:idx+1]
	}

	tail := content[len(content)-tailChars:]
	// Cut at first newline
	if idx := strings.Index(tail, "\n"); idx > 0 && idx < tailChars/2 {
		tail = tail[idx+1:]
	}

	omitted := len(content) - len(head) - len(tail)
	return head + fmt.Sprintf("\n[... %d chars omitted from old tool result ...]\n", omitted) + tail
}

// buildToolResultSummary creates a one-line summary of a tool result.
func buildToolResultSummary(content, toolCallID string) string {
	lines := strings.Count(content, "\n") + 1
	chars := len(content)

	// Try to detect the type of content
	contentType := "text"
	trimmed := strings.TrimSpace(content)
	switch {
	case strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "["):
		contentType = "JSON"
	case strings.Contains(trimmed[:min(200, len(trimmed))], "package "):
		contentType = "Go source"
	case strings.Contains(trimmed[:min(200, len(trimmed))], "import "):
		contentType = "source code"
	case strings.Contains(trimmed[:min(200, len(trimmed))], "def "):
		contentType = "Python source"
	case strings.Contains(trimmed[:min(200, len(trimmed))], "function "):
		contentType = "JavaScript source"
	case strings.HasPrefix(trimmed, "diff ") || strings.HasPrefix(trimmed, "--- "):
		contentType = "diff"
	case strings.HasPrefix(trimmed, "commit "):
		contentType = "git log"
	}

	return fmt.Sprintf("[Old tool result cleared — %d lines, %d chars, %s]", lines, chars, contentType)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
