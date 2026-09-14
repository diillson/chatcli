/*
 * ChatCLI - Tool Result Budget Enforcement
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Enforces aggregate size limits on tool results before sending to the API.
 * Large results are persisted to disk and replaced with compact references,
 * preventing context window saturation.
 *
 * The enforcement runs on the OUTGOING copy of the history, on every
 * request, so the same oversized result is truncated again and again. That
 * is only harmless if the preview comes out byte-identical each time: the
 * result sits ahead of the provider's rolling cache breakpoint, and a
 * preview that differs from the previous request's rewrites the prefix
 * from that message onward — the whole conversation after it is billed as
 * a cache write instead of a read, on every turn, until the result ages
 * out. The overflow file is therefore named by the result's own content
 * (tool-call id plus a content hash), written once and reused, never by a
 * counter.
 *
 * Inspired by openclaude's tool result budget enforcement and
 * MAX_TOOL_RESULTS_PER_MESSAGE_CHARS threshold.
 */
package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

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
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			DefaultTurnBudgetChars = n
		}
	}
	if v := os.Getenv("CHATCLI_TOOL_RESULT_MAX_CHARS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			DefaultPerResultMaxChars = n
		}
	}
}

// budgetResultDirOverride is set by the session workspace so overflow files
// land inside the session scratch area (which is on the read allowlist for
// the agent) instead of a shared global dir. Empty = fall back to default.
var (
	budgetResultDirOverride   string
	budgetResultDirOverrideMu sync.RWMutex
)

// SetBudgetResultDir overrides the directory where EnforceToolResultBudget
// persists overflow files. Pass an empty string to reset to default.
// This is wired by cli/agent.InitSessionWorkspace.
func SetBudgetResultDir(dir string) {
	budgetResultDirOverrideMu.Lock()
	defer budgetResultDirOverrideMu.Unlock()
	budgetResultDirOverride = dir
}

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
	return EnforceToolResultBudgetWith(history, DefaultTurnBudgetChars, DefaultPerResultMaxChars, logger)
}

// EnforceToolResultBudgetWith is EnforceToolResultBudget with explicit
// budgets, so a session can tighten its own limits (context recovery)
// without touching the package defaults shared by every other session
// of the process.
func EnforceToolResultBudgetWith(history []models.Message, turnBudget, perResult int, logger *zap.Logger) ([]models.Message, *BudgetReport) {
	if turnBudget <= 0 {
		turnBudget = DefaultTurnBudgetChars
	}
	if perResult <= 0 {
		perResult = DefaultPerResultMaxChars
	}
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

			if len(msg.Content) > perResult {
				history[idx].Content = truncateWithDiskPersist(
					msg.Content, msg.ToolCallID, perResult, logger)
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

		if turnSize <= turnBudget {
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
			if turnSize <= turnBudget {
				break
			}

			content := history[sr.idx].Content
			// Target: reduce this result to bring turn under budget
			excess := turnSize - turnBudget
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
//
// The preview is a pure function of the content and the tool-call id: the
// file name carries a hash of the content instead of a counter, and a file
// that already holds this content is reused rather than rewritten. Every
// request that re-derives the preview for the same result produces the
// same bytes, which is what keeps the message a cache read instead of a
// prefix rewrite (see the file header).
func truncateWithDiskPersist(content, toolCallID string, maxSize int, logger *zap.Logger) string {
	if len(content) <= maxSize {
		return content
	}

	// Save full content to disk
	dir := budgetResultDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		if logger != nil {
			logger.Warn("Failed to create budget result directory", zap.Error(err))
		}
		return content[:maxSize] + "\n... [output truncated — dir creation failed]"
	}

	fullPath := filepath.Join(dir, overflowFileName(toolCallID, content))

	if err := writeOverflowOnce(fullPath, content); err != nil {
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

	fmt.Fprintf(&preview, "\n\n... [%d chars omitted — full output saved to %s]\n\n",
		len(content)-PreviewHeadChars-PreviewTailChars, fullPath)

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

// overflowFileName names the overflow file for one tool result. The name is
// derived from the tool-call id and a digest of the content, so the same
// result always maps to the same file — across requests of one session,
// and across a tool-call id a provider reuses for different results (Kimi
// K3 does) without the two ever colliding.
func overflowFileName(toolCallID, content string) string {
	id := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		}
		return '_'
	}, strings.TrimSpace(toolCallID))
	if id == "" {
		id = "result"
	}
	sum := sha256.Sum256([]byte(content))
	return fmt.Sprintf("budget_%s_%s.txt", id, hex.EncodeToString(sum[:])[:16])
}

// writeOverflowOnce writes content to path unless a file of the same size
// is already there: the name already commits to the content, so an
// existing file of the right size is this content, written by an earlier
// request. Rewriting it would only cost the bytes.
func writeOverflowOnce(path, content string) error {
	if st, err := os.Stat(path); err == nil && st.Mode().IsRegular() && st.Size() == int64(len(content)) {
		return nil
	}
	return os.WriteFile(path, []byte(content), 0o600)
}

func budgetResultDir() string {
	budgetResultDirOverrideMu.RLock()
	override := budgetResultDirOverride
	budgetResultDirOverrideMu.RUnlock()
	if override != "" {
		return override
	}
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
