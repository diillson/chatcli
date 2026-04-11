/*
 * ChatCLI - Tool Result Budget Enforcement
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Enforces aggregate size limits on tool results before sending to the API.
 * Large results are persisted to disk and replaced with compact references,
 * preventing context window saturation.
 *
 * Inspired by openclaude's tool result budget enforcement and
 * MAX_TOOL_RESULTS_PER_MESSAGE_CHARS threshold.
 */
package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// Budget configuration — configurable via environment variables.
var (
	// DefaultTurnBudgetChars is the maximum aggregate size of all tool results
	// in a single conversation turn (assistant→tool_results group).
	// Tool results exceeding this are persisted to disk and replaced with previews.
	// Override via CHATCLI_TOOL_RESULT_BUDGET_CHARS.
	DefaultTurnBudgetChars = 200_000

	// DefaultPerResultMaxChars is the maximum size of a single tool result.
	// Override via CHATCLI_TOOL_RESULT_MAX_CHARS.
	DefaultPerResultMaxChars = 20_000

	// PreviewHeadChars is how much of a large result to keep as inline preview.
	PreviewHeadChars = 4_000

	// PreviewTailChars is how much of the end to keep for context.
	PreviewTailChars = 1_000
)

func init() {
	if v := os.Getenv("CHATCLI_TOOL_RESULT_BUDGET_CHARS"); v != "" {
		fmt.Sscanf(v, "%d", &DefaultTurnBudgetChars)
	}
	if v := os.Getenv("CHATCLI_TOOL_RESULT_MAX_CHARS"); v != "" {
		fmt.Sscanf(v, "%d", &DefaultPerResultMaxChars)
	}
}

var budgetFileCounter uint64

// BudgetReport describes what the budget enforcement did.
type BudgetReport struct {
	TotalToolResults     int
	TotalOriginalChars   int64
	TotalFinalChars      int64
	ResultsTruncated     int
	ResultsPersistedDisk int
	BytesSavedToDisk     int64
}

// EnforceToolResultBudget scans the conversation history and truncates
// oversized tool results in-place. Large results are persisted to temporary
// files and replaced with compact previews containing a file reference.
//
// The function works in two passes:
//  1. Per-result enforcement: any single result exceeding DefaultPerResultMaxChars
//     is truncated with a preview.
//  2. Per-turn enforcement: if the aggregate of all tool results in a turn
//     exceeds DefaultTurnBudgetChars, the largest results are progressively
//     truncated until the turn fits within budget.
//
// Returns the (possibly modified) history and a report.
func EnforceToolResultBudget(history []models.Message, logger *zap.Logger) ([]models.Message, *BudgetReport) {
	report := &BudgetReport{}

	if len(history) == 0 {
		return history, report
	}

	// Group tool results by the assistant message they respond to.
	// A "turn" is: assistant message with ToolCalls + all following tool messages.
	type turnGroup struct {
		assistantIdx int
		toolIndices  []int
	}

	var turns []turnGroup
	var current *turnGroup

	for i, msg := range history {
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			if current != nil {
				turns = append(turns, *current)
			}
			current = &turnGroup{assistantIdx: i}
		} else if msg.Role == "tool" && current != nil {
			current.toolIndices = append(current.toolIndices, i)
		} else if msg.Role == "assistant" || msg.Role == "user" || msg.Role == "system" {
			if current != nil {
				turns = append(turns, *current)
				current = nil
			}
		}
	}
	if current != nil {
		turns = append(turns, *current)
	}

	// Pass 1: Per-result enforcement
	for _, turn := range turns {
		for _, idx := range turn.toolIndices {
			msg := &history[idx]
			report.TotalToolResults++
			report.TotalOriginalChars += int64(len(msg.Content))

			if len(msg.Content) > DefaultPerResultMaxChars {
				history[idx].Content = truncateWithDiskPersist(
					msg.Content, msg.ToolCallID, DefaultPerResultMaxChars, logger)
				report.ResultsTruncated++
				report.ResultsPersistedDisk++
				report.BytesSavedToDisk += int64(len(msg.Content)) - int64(len(history[idx].Content))
			}
		}
	}

	// Pass 2: Per-turn aggregate enforcement
	for _, turn := range turns {
		turnSize := 0
		for _, idx := range turn.toolIndices {
			turnSize += len(history[idx].Content)
		}

		if turnSize <= DefaultTurnBudgetChars {
			continue
		}

		// Sort tool result indices by size (largest first) for progressive truncation
		type sizedResult struct {
			idx  int
			size int
		}
		sorted := make([]sizedResult, len(turn.toolIndices))
		for i, idx := range turn.toolIndices {
			sorted[i] = sizedResult{idx: idx, size: len(history[idx].Content)}
		}
		sort.Slice(sorted, func(i, j int) bool {
			return sorted[i].size > sorted[j].size
		})

		// Progressively truncate the largest results until under budget
		for _, sr := range sorted {
			if turnSize <= DefaultTurnBudgetChars {
				break
			}

			content := history[sr.idx].Content
			// Target: reduce this result to bring turn under budget
			excess := turnSize - DefaultTurnBudgetChars
			targetSize := len(content) - excess
			if targetSize < PreviewHeadChars+PreviewTailChars+200 {
				targetSize = PreviewHeadChars + PreviewTailChars + 200
			}
			if targetSize >= len(content) {
				continue
			}

			history[sr.idx].Content = truncateWithDiskPersist(
				content, history[sr.idx].ToolCallID, targetSize, logger)

			saved := len(content) - len(history[sr.idx].Content)
			turnSize -= saved
			report.ResultsTruncated++
			report.ResultsPersistedDisk++
			report.BytesSavedToDisk += int64(saved)
		}
	}

	// Compute final chars
	for _, turn := range turns {
		for _, idx := range turn.toolIndices {
			report.TotalFinalChars += int64(len(history[idx].Content))
		}
	}

	if logger != nil && report.ResultsTruncated > 0 {
		logger.Info("Tool result budget enforcement applied",
			zap.Int("results_truncated", report.ResultsTruncated),
			zap.Int64("bytes_saved_to_disk", report.BytesSavedToDisk),
			zap.Int64("original_chars", report.TotalOriginalChars),
			zap.Int64("final_chars", report.TotalFinalChars))
	}

	return history, report
}

// truncateWithDiskPersist saves the full content to a temp file and returns
// a truncated preview with a reference to the file.
func truncateWithDiskPersist(content, toolCallID string, maxSize int, logger *zap.Logger) string {
	if len(content) <= maxSize {
		return content
	}

	// Save full content to disk
	dir := budgetResultDir()
	_ = os.MkdirAll(dir, 0o700)

	n := atomic.AddUint64(&budgetFileCounter, 1)
	sanitizedID := strings.ReplaceAll(toolCallID, "/", "_")
	filename := fmt.Sprintf("budget_%s_%d.txt", sanitizedID, n)
	fullPath := filepath.Join(dir, filename)

	if err := os.WriteFile(fullPath, []byte(content), 0o600); err != nil {
		if logger != nil {
			logger.Warn("Failed to persist tool result to disk, truncating without reference",
				zap.Error(err))
		}
		// Fall back to hard truncation
		return content[:maxSize] + "\n... [output truncated — disk write failed]"
	}

	// Build preview: head + tail + reference
	var preview strings.Builder

	headEnd := PreviewHeadChars
	if headEnd > len(content) {
		headEnd = len(content)
	}

	head := content[:headEnd]
	// Cut at last newline for cleaner output
	if lastNL := strings.LastIndex(head, "\n"); lastNL > headEnd/2 {
		head = head[:lastNL+1]
	}
	preview.WriteString(head)

	preview.WriteString(fmt.Sprintf(
		"\n\n... [%d chars omitted — full output saved to %s]\n\n",
		len(content)-PreviewHeadChars-PreviewTailChars, fullPath))

	tailStart := len(content) - PreviewTailChars
	if tailStart < 0 {
		tailStart = 0
	}
	if tailStart > headEnd {
		tail := content[tailStart:]
		// Cut at first newline for cleaner output
		if firstNL := strings.Index(tail, "\n"); firstNL > 0 && firstNL < len(tail)/2 {
			tail = tail[firstNL+1:]
		}
		preview.WriteString(tail)
	}

	return preview.String()
}

func budgetResultDir() string {
	return filepath.Join(os.TempDir(), "chatcli-tool-results")
}

// CleanupBudgetFiles removes temporary budget enforcement files.
func CleanupBudgetFiles() {
	dir := budgetResultDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "budget_") && strings.HasSuffix(e.Name(), ".txt") {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
