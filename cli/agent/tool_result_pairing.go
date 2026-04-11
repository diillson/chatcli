/*
 * ChatCLI - Tool Result Pairing Validator
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Ensures every tool_use block in the conversation history has a matching
 * tool_result, and every tool_result references a valid tool_use.
 *
 * Inspired by openclaude's ensureToolResultPairing() which cross-message
 * tracks all tool_use IDs and generates synthetic error results for orphans.
 */
package agent

import (
	"fmt"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

const (
	// SyntheticToolResultContent is injected when a tool_use has no matching tool_result.
	SyntheticToolResultContent = "[Tool result missing — the tool execution was interrupted or failed silently. " +
		"Do NOT retry this tool call. Analyze what went wrong and try a different approach.]"

	// OrphanToolResultContent replaces orphaned tool results that reference non-existent tool_use IDs.
	OrphanToolResultContent = "[Orphaned tool result — no matching tool call found. This result has been discarded.]"
)

// PairingRepairReport describes what the pairing validator repaired.
type PairingRepairReport struct {
	SyntheticResultsInjected int      // tool_use blocks without matching tool_result
	OrphanResultsRemoved     int      // tool_result blocks without matching tool_use
	DuplicateToolUsePruned   int      // duplicate tool_use IDs across messages
	MissingToolUseIDs        []string // IDs of tool calls that had no result
	OrphanToolResultIDs      []string // IDs of tool results that had no call
}

// HasRepairs returns true if any repairs were made.
func (r *PairingRepairReport) HasRepairs() bool {
	return r.SyntheticResultsInjected > 0 ||
		r.OrphanResultsRemoved > 0 ||
		r.DuplicateToolUsePruned > 0
}

// EnsureToolResultPairing validates and repairs the conversation history to ensure:
//
//  1. Every assistant message with ToolCalls has corresponding tool result messages
//     with matching ToolCallIDs following it (before the next assistant message).
//  2. Every tool result message references a tool_use ID that exists in a preceding
//     assistant message.
//  3. No duplicate tool_use IDs exist across the conversation.
//
// Repairs:
//   - Missing tool results: injects synthetic error tool_result messages
//   - Orphan tool results: removes them from history
//   - Duplicate tool_use IDs: prunes all but the first occurrence
//
// Returns the repaired history and a report of what was changed.
// If no repairs are needed, returns the original slice unchanged.
func EnsureToolResultPairing(history []models.Message, logger *zap.Logger) ([]models.Message, *PairingRepairReport) {
	report := &PairingRepairReport{}

	if len(history) == 0 {
		return history, report
	}

	// Phase 1: Collect all tool_use IDs and their positions
	type toolUseInfo struct {
		id       string
		msgIndex int
		name     string
	}

	allToolUses := make(map[string]toolUseInfo) // id → info (first occurrence)
	allToolResults := make(map[string]int)      // toolCallID → message index
	seenToolUseIDs := make(map[string]bool)     // for dedup detection

	for i, msg := range history {
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			for _, tc := range msg.ToolCalls {
				if tc.ID == "" {
					continue
				}
				if seenToolUseIDs[tc.ID] {
					report.DuplicateToolUsePruned++
					continue
				}
				seenToolUseIDs[tc.ID] = true
				allToolUses[tc.ID] = toolUseInfo{id: tc.ID, msgIndex: i, name: tc.Name}
			}
		}
		if msg.Role == "tool" && msg.ToolCallID != "" {
			allToolResults[msg.ToolCallID] = i
		}
	}

	// Phase 2: Find mismatches
	// tool_use without tool_result
	missingResults := make(map[string]toolUseInfo)
	for id, info := range allToolUses {
		if _, hasResult := allToolResults[id]; !hasResult {
			missingResults[id] = info
			report.MissingToolUseIDs = append(report.MissingToolUseIDs, id)
		}
	}

	// tool_result without tool_use
	orphanResults := make(map[string]int)
	for resultID, idx := range allToolResults {
		if _, hasUse := allToolUses[resultID]; !hasUse {
			orphanResults[resultID] = idx
			report.OrphanToolResultIDs = append(report.OrphanToolResultIDs, resultID)
		}
	}

	report.SyntheticResultsInjected = len(missingResults)
	report.OrphanResultsRemoved = len(orphanResults)

	if !report.HasRepairs() {
		return history, report
	}

	// Phase 3: Build repaired history
	repaired := make([]models.Message, 0, len(history)+len(missingResults))

	// Track which orphan indices to skip
	orphanIndices := make(map[int]bool)
	for _, idx := range orphanResults {
		orphanIndices[idx] = true
	}

	for i, msg := range history {
		// Skip orphaned tool results
		if orphanIndices[i] {
			if logger != nil {
				logger.Warn("Removed orphaned tool result",
					zap.String("tool_call_id", msg.ToolCallID),
					zap.Int("message_index", i))
			}
			continue
		}

		// Handle duplicate tool_use IDs in assistant messages
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 && report.DuplicateToolUsePruned > 0 {
			seen := make(map[string]bool)
			dedupCalls := make([]models.ToolCall, 0, len(msg.ToolCalls))
			for _, tc := range msg.ToolCalls {
				if tc.ID != "" && seen[tc.ID] {
					continue
				}
				seen[tc.ID] = true
				dedupCalls = append(dedupCalls, tc)
			}
			msg.ToolCalls = dedupCalls
		}

		repaired = append(repaired, msg)

		// After an assistant message with tool calls, inject synthetic results
		// for any tool_use IDs that have no corresponding tool_result
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			for _, tc := range msg.ToolCalls {
				if _, missing := missingResults[tc.ID]; missing {
					synthetic := models.Message{
						Role:       "tool",
						Content:    SyntheticToolResultContent,
						ToolCallID: tc.ID,
					}
					repaired = append(repaired, synthetic)

					if logger != nil {
						logger.Warn("Injected synthetic tool result for orphaned tool_use",
							zap.String("tool_call_id", tc.ID),
							zap.String("tool_name", tc.Name),
							zap.Int("assistant_message_index", i))
					}
				}
			}
		}
	}

	return repaired, report
}

// ValidateToolResultPairing checks if the history has pairing issues without repairing.
// Returns true if the history is valid (no repairs needed).
func ValidateToolResultPairing(history []models.Message) bool {
	_, report := EnsureToolResultPairing(history, nil)
	return !report.HasRepairs()
}

// CountPendingToolCalls returns how many tool calls in the last assistant message
// don't yet have a tool result. Used to detect incomplete tool execution.
func CountPendingToolCalls(history []models.Message) int {
	// Find the last assistant message with tool calls
	var lastToolCalls []models.ToolCall
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "assistant" && len(history[i].ToolCalls) > 0 {
			lastToolCalls = history[i].ToolCalls
			break
		}
	}

	if len(lastToolCalls) == 0 {
		return 0
	}

	// Count results after it
	resultIDs := make(map[string]bool)
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "tool" && history[i].ToolCallID != "" {
			resultIDs[history[i].ToolCallID] = true
		}
		if history[i].Role == "assistant" && len(history[i].ToolCalls) > 0 {
			break // stop at the assistant message
		}
	}

	pending := 0
	for _, tc := range lastToolCalls {
		if !resultIDs[tc.ID] {
			pending++
		}
	}
	return pending
}

// GenerateToolCallID creates a deterministic tool call ID for XML-parsed tool calls
// that don't have native IDs. Uses the turn number and call index for uniqueness.
func GenerateToolCallID(turn, callIndex int) string {
	return fmt.Sprintf("tc_%d_%d", turn, callIndex)
}
