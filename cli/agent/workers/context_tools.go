/*
 * ChatCLI - Worker context tools (recall + read-only memory/session/knowledge)
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Workers run in isolated ReAct loops with a small engine-subcommand toolset.
 * This file gives them the same context-navigation surfaces a human (and the
 * orchestrator) already has, without importing the cli package:
 *
 *   - recall: expand <<ccr:KEY>> markers left by the shared CCR compression
 *     layer on truncated tool output (the counterpart of @recall).
 *   - memory-recall / session-search / session-get / knowledge-search /
 *     knowledge-get: read-only views over persistent memory, saved sessions
 *     and attached knowledge bases, granted per-agent (persona frontmatter
 *     tools: Memory, Session, Knowledge) or per-delegation.
 *
 * The cli package wires the actual implementations at startup via the
 * Register* hooks below (same pattern as RegisterToolOutputCompressor); when
 * a hook is absent the corresponding tool is not offered to the model.
 */
package workers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/diillson/chatcli/cli/compress"
	"github.com/diillson/chatcli/models"
)

// Worker context tool subcommand names. These are NOT coder engine
// subcommands: classifyToolCalls admits them via the allowlist and
// executeToolCall routes them to the registered runner instead of the engine.
const (
	ContextToolMemoryRecall    = "memory-recall"
	ContextToolSessionSearch   = "session-search"
	ContextToolSessionGet      = "session-get"
	ContextToolKnowledgeSearch = "knowledge-search"
	ContextToolKnowledgeGet    = "knowledge-get"

	// recallSubcmd is the universal CCR-expansion tool (like mail, offered to
	// every worker whenever a recaller is registered).
	recallSubcmd = "recall"
)

var (
	workerHooksMu sync.RWMutex

	// ccrRecaller expands one CCR key to its original content.
	ccrRecaller func(key string) (string, bool)

	// contextToolRunner executes one read-only context tool call.
	contextToolRunner func(ctx context.Context, tool string, args map[string]interface{}) (string, error)

	// workerContextProvider returns the proactive recall block ([MEMORY
	// AUTO-RECALL] / [SESSION RECALL]) for a worker task, or "".
	workerContextProvider func(task string) string

	// squadCompressionLayer is the session CCR layer, shared so worker-loop
	// microcompact preserves dropped bytes exactly like the orchestrator's.
	squadCompressionLayer *compress.Layer
)

// RegisterCCRRecaller wires (or clears, with nil) the CCR key expander used
// by the worker recall tool. Safe to call at startup.
func RegisterCCRRecaller(fn func(key string) (string, bool)) {
	workerHooksMu.Lock()
	ccrRecaller = fn
	workerHooksMu.Unlock()
}

// RegisterContextToolRunner wires (or clears, with nil) the executor for the
// read-only worker context tools (memory/session/knowledge).
func RegisterContextToolRunner(fn func(ctx context.Context, tool string, args map[string]interface{}) (string, error)) {
	workerHooksMu.Lock()
	contextToolRunner = fn
	workerHooksMu.Unlock()
}

// RegisterWorkerContextProvider wires (or clears, with nil) the provider of
// the proactive recall block injected into each worker's initial context.
func RegisterWorkerContextProvider(fn func(task string) string) {
	workerHooksMu.Lock()
	workerContextProvider = fn
	workerHooksMu.Unlock()
}

// RegisterSquadCompressionLayer shares the session CCR layer with the worker
// ReAct loop so its microcompact archives originals instead of dropping them.
func RegisterSquadCompressionLayer(layer *compress.Layer) {
	workerHooksMu.Lock()
	squadCompressionLayer = layer
	workerHooksMu.Unlock()
}

func currentCCRRecaller() func(string) (string, bool) {
	workerHooksMu.RLock()
	defer workerHooksMu.RUnlock()
	return ccrRecaller
}

func currentContextToolRunner() func(context.Context, string, map[string]interface{}) (string, error) {
	workerHooksMu.RLock()
	defer workerHooksMu.RUnlock()
	return contextToolRunner
}

func currentWorkerContextProvider() func(string) string {
	workerHooksMu.RLock()
	defer workerHooksMu.RUnlock()
	return workerContextProvider
}

func currentSquadCompressionLayer() *compress.Layer {
	workerHooksMu.RLock()
	defer workerHooksMu.RUnlock()
	return squadCompressionLayer
}

// isContextToolSubcmd reports whether subcmd is one of the read-only worker
// context tools served by the registered runner.
func isContextToolSubcmd(subcmd string) bool {
	switch subcmd {
	case ContextToolMemoryRecall, ContextToolSessionSearch, ContextToolSessionGet,
		ContextToolKnowledgeSearch, ContextToolKnowledgeGet:
		return true
	}
	return false
}

// RecallToolDefinition is the native tool definition for CCR expansion.
// Offered to every worker whenever a recaller is registered (universal, like
// send_mail): compressed/truncated tool output carries <<ccr:KEY>> markers
// and the worker must be able to walk them back.
func RecallToolDefinition() models.ToolDefinition {
	return models.ToolDefinition{
		Type: "function",
		Function: models.ToolFunctionDef{
			Name: "recall_output",
			Description: "Expand compressed or truncated tool output back to its original form. " +
				"Whenever a result contains one or more <<ccr:KEY>> markers, pass them here to retrieve the full original content. " +
				"Accepts a single key, a marker, or a string containing several markers.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"keys": map[string]interface{}{
						"type":        "string",
						"description": "One or more CCR keys or <<ccr:KEY>> markers (any surrounding text is tolerated).",
					},
				},
				"required": []string{"keys"},
			},
		},
	}
}

// contextToolDefinitionsCatalog builds the native definitions for the
// read-only context tools, keyed by subcommand name.
func contextToolDefinitionsCatalog() map[string]models.ToolDefinition {
	return map[string]models.ToolDefinition{
		ContextToolMemoryRecall: {
			Type: "function",
			Function: models.ToolFunctionDef{
				Name: "memory_recall",
				Description: "Search the user's persistent cross-session memory (facts, preferences, project history) for entries relevant to a query. " +
					"Read-only. Use when the task references prior decisions, conventions or context you do not see in the current conversation.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"query": map[string]interface{}{
							"type":        "string",
							"description": "What to look for (keywords or a short question).",
						},
					},
					"required": []string{"query"},
				},
			},
		},
		ContextToolSessionSearch: {
			Type: "function",
			Function: models.ToolFunctionDef{
				Name: "session_search",
				Description: "Search saved past conversations for a query; returns matching session names with snippets. " +
					"Read-only. Follow up with session_get to read the matching part.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"query": map[string]interface{}{
							"type":        "string",
							"description": "Free-text search across saved sessions.",
						},
						"limit": map[string]interface{}{
							"type":        "integer",
							"description": "Max snippets per session (default 3).",
						},
					},
					"required": []string{"query"},
				},
			},
		},
		ContextToolSessionGet: {
			Type: "function",
			Function: models.ToolFunctionDef{
				Name: "session_get",
				Description: "Read one saved session page by page. Read-only. " +
					"query jumps to the best-matching message; message=<index> returns that single message nearly in full (use for truncated entries).",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"name": map[string]interface{}{
							"type":        "string",
							"description": "Saved session name (from session_search).",
						},
						"offset": map[string]interface{}{
							"type":        "integer",
							"description": "0-based message offset (default 0).",
						},
						"limit": map[string]interface{}{
							"type":        "integer",
							"description": "Messages per page (default 20).",
						},
						"query": map[string]interface{}{
							"type":        "string",
							"description": "Jump to the best-matching message instead of using offset.",
						},
						"message": map[string]interface{}{
							"type":        "integer",
							"description": "Absolute message index: return THAT message alone, nearly untruncated.",
						},
					},
					"required": []string{"name"},
				},
			},
		},
		ContextToolKnowledgeSearch: {
			Type: "function",
			Function: models.ToolFunctionDef{
				Name:        "knowledge_search",
				Description: "Search the session's attached knowledge bases (indexed docs/corpora) for relevant segments. Read-only.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"query": map[string]interface{}{
							"type":        "string",
							"description": "What to look for.",
						},
						"top_k": map[string]interface{}{
							"type":        "integer",
							"description": "Number of segments to return (default 5).",
						},
						"kb": map[string]interface{}{
							"type":        "string",
							"description": "Knowledge base name (optional when only one is attached).",
						},
					},
					"required": []string{"query"},
				},
			},
		},
		ContextToolKnowledgeGet: {
			Type: "function",
			Function: models.ToolFunctionDef{
				Name:        "knowledge_get",
				Description: "Read a whole document from an attached knowledge base, paginated by offset. Read-only. Use after knowledge_search.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"source": map[string]interface{}{
							"type":        "string",
							"description": "Document source path/name (from knowledge_search results).",
						},
						"offset": map[string]interface{}{
							"type":        "integer",
							"description": "Pagination offset (default 0; the previous page reports the next offset).",
						},
						"kb": map[string]interface{}{
							"type":        "string",
							"description": "Knowledge base name (optional when only one is attached).",
						},
					},
					"required": []string{"source"},
				},
			},
		},
	}
}

// ContextToolDefinitions returns the native tool definitions for the
// read-only context tools present in allowedCmds. Tools are only offered
// when a runner is registered — a definition without an executor would
// guarantee a failing call.
func ContextToolDefinitions(allowedCmds []string) []models.ToolDefinition {
	if currentContextToolRunner() == nil {
		return nil
	}
	catalog := contextToolDefinitionsCatalog()
	result := make([]models.ToolDefinition, 0, 2)
	for _, cmd := range allowedCmds {
		if td, ok := catalog[cmd]; ok {
			result = append(result, td)
		}
	}
	return result
}

// resolvedCallArgs returns the structured args of a resolved tool call,
// tolerating the XML-mode {"cmd":X,"args":{...}} envelope and flat JSON.
func resolvedCallArgs(rtc resolvedToolCall) map[string]interface{} {
	if len(rtc.NativeArgs) > 0 {
		return rtc.NativeArgs
	}
	raw := strings.TrimSpace(rtc.RawArgs)
	if raw == "" {
		return nil
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil
	}
	if inner, ok := parsed["args"].(map[string]interface{}); ok {
		return inner
	}
	return parsed
}

// executeRecall handles the recall tool call: it extracts every CCR key from
// the args (lenient — bare keys, markers, or text containing markers) and
// expands each via the registered recaller. Recalled content bypasses the
// CCR compressor (re-compressing what the model explicitly asked to see
// would loop) but still overflows to disk past the inline limit.
func executeRecall(v validatedTC) execResult {
	fail := func(err error) execResult {
		record := ToolCallRecord{Name: recallSubcmd, Args: v.rtc.RawArgs, Error: err}
		return execResult{index: v.index, record: record, output: fmt.Sprintf("[recall] %v\n", err), failed: true, toolID: v.rtc.ID}
	}

	recaller := currentCCRRecaller()
	if recaller == nil {
		return fail(errors.New("recall is not available in this session"))
	}

	args := resolvedCallArgs(v.rtc)
	var raw strings.Builder
	for _, k := range []string{"keys", "key", "marker", "markers", "content"} {
		if s, ok := args[k].(string); ok && strings.TrimSpace(s) != "" {
			raw.WriteString(s)
			raw.WriteString(" ")
		}
	}
	if raw.Len() == 0 {
		// Last resort: scan the raw args string itself for markers/keys.
		raw.WriteString(v.rtc.RawArgs)
	}

	keys := compress.ExtractKeys(raw.String())
	if len(keys) == 0 {
		// Tolerate bare keys without the <<ccr:>> wrapper: pick tokens that
		// look like CCR keys (16 lowercase hex chars).
		for _, tok := range strings.FieldsFunc(raw.String(), func(r rune) bool {
			return !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f'))
		}) {
			if len(tok) == 16 {
				keys = append(keys, tok)
			}
		}
	}
	if len(keys) == 0 {
		return fail(errors.New(`no CCR keys found in args — pass "keys" with one or more <<ccr:KEY>> markers`))
	}

	var out strings.Builder
	misses := 0
	for i, key := range keys {
		content, ok := recaller(key)
		if !ok {
			misses++
			fmt.Fprintf(&out, "[recall %s] not found (expired or unknown key)\n", key)
			continue
		}
		if len(keys) > 1 {
			fmt.Fprintf(&out, "--- recalled %s (%d/%d) ---\n", key, i+1, len(keys))
		}
		out.WriteString(content)
		if !strings.HasSuffix(content, "\n") {
			out.WriteString("\n")
		}
	}

	output := overflowToDisk(recallSubcmd, out.String(), MaxInlineResultBytes)
	record := ToolCallRecord{Name: recallSubcmd, Args: v.rtc.RawArgs, Output: output}
	if misses == len(keys) {
		record.Error = errors.New("no CCR key could be expanded")
		return execResult{index: v.index, record: record, output: "[recall] " + output, failed: true, toolID: v.rtc.ID}
	}
	return execResult{index: v.index, record: record, output: "[recall] " + output, toolID: v.rtc.ID}
}

// executeContextTool handles one read-only context tool call via the
// registered runner, applying the standard result truncation policy.
func executeContextTool(ctx context.Context, v validatedTC) execResult {
	runner := currentContextToolRunner()
	if runner == nil {
		err := fmt.Errorf("%s is not available in this session", v.rtc.Subcmd)
		record := ToolCallRecord{Name: v.rtc.Subcmd, Args: v.rtc.RawArgs, Error: err}
		return execResult{index: v.index, record: record, output: fmt.Sprintf("[%s] %v\n", v.rtc.Subcmd, err), failed: true, toolID: v.rtc.ID}
	}

	output, err := runner(ctx, v.rtc.Subcmd, resolvedCallArgs(v.rtc))
	if err != nil {
		record := ToolCallRecord{Name: v.rtc.Subcmd, Args: v.rtc.RawArgs, Error: err}
		return execResult{index: v.index, record: record, output: fmt.Sprintf("[%s] %v\n", v.rtc.Subcmd, err), failed: true, toolID: v.rtc.ID}
	}

	output = TruncateToolResult(v.rtc.Subcmd, output)
	record := ToolCallRecord{Name: v.rtc.Subcmd, Args: v.rtc.RawArgs, Output: output}
	return execResult{index: v.index, record: record, output: fmt.Sprintf("[%s] %s\n", v.rtc.Subcmd, output), toolID: v.rtc.ID}
}
