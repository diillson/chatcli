/*
 * ChatCLI - Tool Result Pairing Validator
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Ensures every tool_use block in the conversation history has a matching
 * tool_result, and every tool_result references a valid tool_use.
 *
 * Pairing is POSITIONAL (block-scoped), not global: each assistant message
 * that carries tool_calls owns the stretch of history up to the next
 * assistant message, and its results are matched only inside that stretch.
 * This is required for providers whose tool_call IDs are NOT globally
 * unique — Moonshot/Kimi K3 emits deterministic per-turn IDs such as
 * "web_fetch:0"/"web_fetch:1" that repeat on every turn, so a global
 * id→result map silently pairs an old dangling call with a newer turn's
 * result and ships an invalid history (API 400: "tool_call_ids did not
 * have response messages").
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

	// SyntheticToolResultErrorCode marks injected synthetic results so
	// providers surface them as errors (OpenAI-family [ERROR:...] marker,
	// Anthropic is_error) instead of apparent successes, and so downstream
	// consumers can distinguish repair synthetics from real outputs.
	SyntheticToolResultErrorCode = "missing_result"

	// OrphanToolResultContent is kept for API compatibility.
	//
	// Deprecated: orphaned tool results are REMOVED from history, never
	// replaced with this placeholder. No code references it.
	OrphanToolResultContent = "[Orphaned tool result — no matching tool call found. This result has been discarded.]"
)

// PairingRepairReport describes what the pairing validator repaired.
type PairingRepairReport struct {
	SyntheticResultsInjected int      // tool_use blocks without matching tool_result
	OrphanResultsRemoved     int      // tool_result blocks without matching tool_use
	DuplicateToolUsePruned   int      // duplicate or empty tool_use IDs within a single assistant message
	ResultsRelocated         int      // results moved next to their tool_call past interposed messages
	MissingToolUseIDs        []string // IDs of tool calls that had no result
	OrphanToolResultIDs      []string // IDs of tool results that had no call
}

// HasRepairs returns true if any repairs were made.
func (r *PairingRepairReport) HasRepairs() bool {
	return r.SyntheticResultsInjected > 0 ||
		r.OrphanResultsRemoved > 0 ||
		r.DuplicateToolUsePruned > 0 ||
		r.ResultsRelocated > 0
}

// EnsureToolResultPairing validates and repairs the conversation history so that
// every assistant message carrying ToolCalls is immediately followed by exactly
// one tool result message per call — the shape every native tool API requires.
//
// Scope rules (per assistant block, in order):
//
//  1. Results are claimed from the stretch between the assistant message and
//     the NEXT assistant message. A matching result that sits past interposed
//     user/system messages is relocated to be adjacent to its call.
//  2. Calls with no claimable result get a synthetic error result injected
//     directly after the assistant message.
//  3. Duplicate IDs WITHIN one assistant message are pruned (all but the
//     first), and calls with an empty ID are pruned outright — they can never
//     be paired, and serializing them ships an invalid empty id. The same ID
//     reappearing in a LATER assistant message is legal — providers like
//     Moonshot/Kimi reuse deterministic per-turn IDs.
//  4. Tool result messages not claimed by any block are orphans and removed.
//
// Returns the repaired history and a report of what was changed.
// If no repairs are needed, returns the original slice unchanged — the common
// case is allocation-free via a validation prepass, since this runs on the
// persistent history every agent turn.
func EnsureToolResultPairing(history []models.Message, logger *zap.Logger) ([]models.Message, *PairingRepairReport) {
	report := &PairingRepairReport{}

	if len(history) == 0 || !pairingNeedsRepair(history) {
		return history, report
	}

	claimed := make([]bool, len(history)) // tool results claimed by an assistant block
	repaired := make([]models.Message, 0, len(history)+4)

	for i := 0; i < len(history); i++ {
		msg := history[i]

		if msg.Role == "tool" {
			if claimed[i] {
				continue // already emitted next to its assistant block
			}
			// Unclaimed tool result: no preceding block owns this ID → orphan.
			report.OrphanResultsRemoved++
			report.OrphanToolResultIDs = append(report.OrphanToolResultIDs, msg.ToolCallID)
			if logger != nil {
				logger.Warn("Removed orphaned tool result",
					zap.String("tool_call_id", msg.ToolCallID),
					zap.Int("message_index", i))
			}
			continue
		}

		if msg.Role != "assistant" || len(msg.ToolCalls) == 0 {
			repaired = append(repaired, msg)
			continue
		}

		// Assistant block: prune empty-ID and intra-message duplicate IDs,
		// then claim results from the stretch up to the next assistant message.
		calls := pruneInvalidCalls(msg.ToolCalls, report)
		if len(calls) < len(msg.ToolCalls) {
			msg.ToolCalls = calls
		}
		repaired = append(repaired, msg)

		// Claim pass: first matching unclaimed result per call ID, scanning
		// only until the next assistant message (that one owns what follows).
		// A claim made after a non-tool message was passed is a relocation.
		wanted := make(map[string]bool, len(calls))
		for _, tc := range calls {
			wanted[tc.ID] = true
		}
		type claim struct {
			idx       int
			relocated bool
		}
		found := make(map[string]claim, len(calls))
		interposed := false
		for j := i + 1; j < len(history); j++ {
			if history[j].Role == "assistant" {
				break
			}
			if history[j].Role != "tool" {
				interposed = true
				continue
			}
			id := history[j].ToolCallID
			if id == "" || !wanted[id] {
				continue
			}
			if _, dup := found[id]; dup {
				continue
			}
			found[id] = claim{idx: j, relocated: interposed}
			claimed[j] = true
		}

		// Emission pass: one result per call, in call order — real when
		// claimed, synthetic otherwise.
		for _, tc := range calls {
			if c, ok := found[tc.ID]; ok {
				repaired = append(repaired, history[c.idx])
				if c.relocated {
					report.ResultsRelocated++
					if logger != nil {
						logger.Warn("Relocated displaced tool result next to its tool_call",
							zap.String("tool_call_id", tc.ID),
							zap.Int("from_index", c.idx))
					}
				}
				continue
			}
			repaired = append(repaired, models.NewToolResultMessage(
				tc.ID, SyntheticToolResultContent, true, SyntheticToolResultErrorCode))
			report.SyntheticResultsInjected++
			report.MissingToolUseIDs = append(report.MissingToolUseIDs, tc.ID)
			if logger != nil {
				logger.Warn("Injected synthetic tool result for orphaned tool_use",
					zap.String("tool_call_id", tc.ID),
					zap.String("tool_name", tc.Name),
					zap.Int("assistant_message_index", i))
			}
		}
	}

	if !report.HasRepairs() {
		return history, report
	}
	return repaired, report
}

// pruneInvalidCalls drops empty-ID calls and intra-message duplicates (all
// but the first occurrence), counting both in DuplicateToolUsePruned. It
// returns the original slice untouched when nothing needs pruning — the
// overwhelmingly common case.
func pruneInvalidCalls(toolCalls []models.ToolCall, report *PairingRepairReport) []models.ToolCall {
	dirty := false
	for i, tc := range toolCalls {
		if tc.ID == "" {
			dirty = true
			break
		}
		for j := 0; j < i; j++ {
			if toolCalls[j].ID == tc.ID {
				dirty = true
				break
			}
		}
		if dirty {
			break
		}
	}
	if !dirty {
		return toolCalls
	}

	seen := make(map[string]bool, len(toolCalls))
	calls := make([]models.ToolCall, 0, len(toolCalls))
	for _, tc := range toolCalls {
		if tc.ID == "" || seen[tc.ID] {
			report.DuplicateToolUsePruned++
			continue
		}
		seen[tc.ID] = true
		calls = append(calls, tc)
	}
	return calls
}

// pairingNeedsRepair is the allocation-light validation prepass: it returns
// false exactly when the repair pass would report no repairs, so the caller
// can keep the original slice. Mirrors the repair semantics: every assistant
// block's calls must have non-empty unique IDs and be answered by STRICTLY
// adjacent tool messages (relocation counts as a repair), and every tool
// message must be part of such an adjacent run with a matching ID.
func pairingNeedsRepair(history []models.Message) bool {
	i := 0
	for i < len(history) {
		msg := history[i]
		if msg.Role == "tool" {
			return true // not consumed by a block below → orphan or displaced
		}
		if msg.Role != "assistant" || len(msg.ToolCalls) == 0 {
			i++
			continue
		}
		calls := msg.ToolCalls
		for k, tc := range calls {
			if tc.ID == "" {
				return true
			}
			for j := 0; j < k; j++ {
				if calls[j].ID == tc.ID {
					return true
				}
			}
		}
		// Consume the adjacent tool run; every result must match a distinct call.
		matched := 0
		j := i + 1
		for ; j < len(history) && history[j].Role == "tool"; j++ {
			id := history[j].ToolCallID
			ok := false
			for _, tc := range calls {
				if tc.ID == id {
					ok = true
					break
				}
			}
			if !ok || matched >= len(calls) {
				return true // extra/foreign result in the adjacent run
			}
			// Duplicate result for the same ID inside the run?
			for k := i + 1; k < j; k++ {
				if history[k].ToolCallID == id {
					return true
				}
			}
			matched++
		}
		if matched != len(calls) {
			return true // missing results (or displaced past interposed prose)
		}
		i = j
	}
	return false
}

// ValidateToolResultPairing checks if the history has pairing issues without repairing.
// Returns true if the history is valid (no repairs needed).
func ValidateToolResultPairing(history []models.Message) bool {
	return !pairingNeedsRepair(history)
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
