/*
 * ChatCLI - Native tool_call result emission helpers
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * When the assistant message carries NATIVE tool_calls, every provider's
 * tool API (Anthropic, OpenAI-compatible: Moonshot, MiniMax, ZAI, ...)
 * requires one tool result per call, adjacent to the assistant message.
 * These helpers guarantee that shape at the two spots where the batch loop
 * can end without a complete result set: a mid-batch error (fail-fast) and
 * an @park suspension.
 */
package cli

import (
	"github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/models"
)

// LLM-facing closure texts for calls that never ran. English on purpose
// (model-facing, not user-facing) and explicit about the recovery move so
// the model re-issues instead of hallucinating an outcome.
const (
	toolResultNotExecutedBatchError = "[Tool call not executed — an earlier action in this batch failed and the " +
		"batch was aborted (fail-fast). Re-issue this call if it is still needed.]"
	toolResultNotExecutedPark = "[Tool call not executed — the agent parked (suspended) before this call ran. " +
		"Re-issue this call after resume if it is still needed.]"
	// toolResultErrorCodeNotExecuted marks closure results for calls that
	// were never dispatched, distinguishing them from real execution errors.
	toolResultErrorCodeNotExecuted = "not_executed"
)

// insertStructuredToolResults inserts role:"tool" messages immediately after
// the last assistant message carrying tool_calls — after any tool results
// already sitting there, but BEFORE any feedback message appended mid-batch
// (security blocks, format-fix prompts). Plain append would strand the
// results behind those user messages and break the adjacency every native
// tool API validates.
func insertStructuredToolResults(history []models.Message, results []models.Message) []models.Message {
	if len(results) == 0 {
		return history
	}
	anchor := -1
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "assistant" && len(history[i].ToolCalls) > 0 {
			anchor = i
			break
		}
	}
	if anchor == -1 {
		return append(history, results...)
	}
	insertAt := anchor + 1
	for insertAt < len(history) && history[insertAt].Role == "tool" {
		insertAt++
	}
	out := make([]models.Message, 0, len(history)+len(results))
	out = append(out, history[:insertAt]...)
	out = append(out, results...)
	out = append(out, history[insertAt:]...)
	return out
}

// buildNativeBatchResults produces one tool result message per native call.
// executed holds the structured outcomes for the batch's executed prefix
// (the loop is fail-fast, so outcomes always align index-for-index with the
// first len(executed) calls); every call beyond it gets a "not executed"
// closure carrying notExecutedContent.
func buildNativeBatchResults(
	nativeToolCalls []models.ToolCall,
	executed []agent.ToolResult,
	notExecutedContent string,
) []models.Message {
	results := make([]models.Message, 0, len(nativeToolCalls))
	for idx, ntc := range nativeToolCalls {
		if idx < len(executed) {
			res := executed[idx]
			results = append(results, models.NewToolResultMessage(ntc.ID, res.Output, res.IsError, res.ErrorCode))
			continue
		}
		results = append(results, models.NewToolResultMessage(
			ntc.ID, notExecutedContent, true, toolResultErrorCodeNotExecuted))
	}
	return results
}

// buildParkBatchClosure closes every native call of the in-flight batch
// EXCEPT the park call itself (parkIdx), which stays pending — RunResumed
// synthesizes its result with the real park outcome at resume time. Without
// this closure the park snapshot inherits dangling tool_calls (the batch
// loop suspends before its structured-emission tail runs) and every resume
// ships an invalid history.
func buildParkBatchClosure(
	nativeToolCalls []models.ToolCall,
	executed []agent.ToolResult,
	parkIdx int,
) []models.Message {
	results := make([]models.Message, 0, len(nativeToolCalls))
	for idx, ntc := range nativeToolCalls {
		if idx == parkIdx {
			continue
		}
		if idx < len(executed) {
			res := executed[idx]
			results = append(results, models.NewToolResultMessage(ntc.ID, res.Output, res.IsError, res.ErrorCode))
			continue
		}
		results = append(results, models.NewToolResultMessage(
			ntc.ID, toolResultNotExecutedPark, true, toolResultErrorCodeNotExecuted))
	}
	return results
}
