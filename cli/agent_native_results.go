/*
 * ChatCLI - Native tool_call result emission helpers
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * When the assistant message carries NATIVE tool_calls, every provider's
 * tool API (Anthropic, OpenAI-compatible: Moonshot, MiniMax, ZAI, ...)
 * requires one tool result per call, adjacent to the assistant message.
 * These helpers guarantee that shape at every spot where the ReAct loop
 * can end a turn without a complete result set: mid-batch errors
 * (fail-fast), @park suspension, context cancellation, stagnation stop,
 * and the agent_call dispatch path that supersedes the batch.
 */
package cli

import (
	"github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/models"
)

// LLM-facing closure texts for calls that never ran. English on purpose
// (model-facing, not user-facing) and explicit about the recovery move so
// the model re-issues instead of hallucinating an outcome.
const (
	toolResultNotExecutedBatchError = "[Tool call not executed — the batch stopped before this call completed " +
		"(an action failed or was blocked; see the accompanying feedback message for the reason). " +
		"Re-issue this call if it is still needed.]"
	toolResultNotExecutedPark = "[Tool call not executed — the agent parked (suspended) before this call ran. " +
		"Re-issue this call after resume if it is still needed.]"
	toolResultNotExecutedCanceled = "[Tool call interrupted — the run was canceled before this call completed. " +
		"Re-issue this call if it is still needed.]"
	toolResultNotExecutedStagnation = "[Tool call not executed — the same tool batch was repeated without new " +
		"information and the loop was stopped (stagnation). Do NOT re-issue the identical call; change approach.]"
	toolResultNotExecutedAgentCalls = "[Tool call not executed — agent_calls were dispatched this turn instead; " +
		"their results are in the following message. Re-issue this call only if the delegated work did not cover it.]"
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
	if insertAt == len(history) {
		// Common case: the block is the tail — a plain append preserves
		// slice capacity instead of copying the whole history.
		return append(history, results...)
	}
	out := make([]models.Message, 0, len(history)+len(results))
	out = append(out, history[:insertAt]...)
	out = append(out, results...)
	out = append(out, history[insertAt:]...)
	return out
}

// buildBatchResults is the single emission core: one tool result message per
// native call, in call order. Calls at an index below len(executed) carry the
// structured outcome (the batch loop is fail-fast, so outcomes always align
// index-for-index with the executed prefix); every other call gets a
// "not executed" closure carrying notExecutedContent. skipIdx (or -1) leaves
// exactly one call unanswered — the @park call, whose result RunResumed
// synthesizes with the real park outcome at resume time.
func buildBatchResults(
	nativeToolCalls []models.ToolCall,
	executed []agent.ToolResult,
	notExecutedContent string,
	skipIdx int,
) []models.Message {
	results := make([]models.Message, 0, len(nativeToolCalls))
	for idx, ntc := range nativeToolCalls {
		if idx == skipIdx {
			continue
		}
		if idx < len(executed) {
			res := executed[idx]
			msg := models.NewToolResultMessage(ntc.ID, res.Output, res.IsError, res.ErrorCode)
			// Native-protocol counterpart of buildBatchFeedbackMessage: a
			// @recall result returns an archived original verbatim, so it
			// must survive trimming and microcompact intact (otherwise the
			// model is forced to recall it again).
			if plugins.IsRecallTool(ntc.Name) {
				msg.Meta = &models.MessageMeta{PreserveVerbatim: true}
			}
			results = append(results, msg)
			continue
		}
		results = append(results, models.NewToolResultMessage(
			ntc.ID, notExecutedContent, true, toolResultErrorCodeNotExecuted))
	}
	return results
}

// buildNativeBatchResults produces one tool result message per native call —
// see buildBatchResults for the alignment contract.
func buildNativeBatchResults(
	nativeToolCalls []models.ToolCall,
	executed []agent.ToolResult,
	notExecutedContent string,
) []models.Message {
	return buildBatchResults(nativeToolCalls, executed, notExecutedContent, -1)
}

// buildParkBatchClosure closes every native call of the in-flight batch
// EXCEPT the park call itself (parkIdx). Without this closure the park
// snapshot inherits dangling tool_calls (the batch loop suspends before its
// structured-emission tail runs) and every resume ships an invalid history.
func buildParkBatchClosure(
	nativeToolCalls []models.ToolCall,
	executed []agent.ToolResult,
	parkIdx int,
) []models.Message {
	return buildBatchResults(nativeToolCalls, executed, toolResultNotExecutedPark, parkIdx)
}

// closeNativeBatch closes every native call of the current turn in one shot —
// the executed prefix with real outcomes, the rest with notExecutedContent —
// inserted adjacent to the assistant message. Used by the loop's non-standard
// exit paths (cancellation, stagnation, agent_call dispatch) so no exit can
// leave dangling tool_calls in the persistent history.
func (a *AgentMode) closeNativeBatch(
	nativeToolCalls []models.ToolCall,
	executed []agent.ToolResult,
	notExecutedContent string,
) {
	if len(nativeToolCalls) == 0 {
		return
	}
	a.cli.history = insertStructuredToolResults(a.cli.history,
		buildNativeBatchResults(nativeToolCalls, executed, notExecutedContent))
}
