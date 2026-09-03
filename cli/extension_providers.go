/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Extension points: external memory providers and context engines.
 *
 * The embedded memory (facts, episodes, graph, auto-recall) and the
 * embedded compactor stay the defaults. An organization that already
 * runs a memory service or a context engine plugs it in through MCP —
 * the transport ChatCLI already speaks — with no ChatCLI code:
 *
 *   CHATCLI_MEMORY_PROVIDER=mcp:<server>
 *     memory_recall(query, hints, budget_chars) → text appended to the
 *       auto-recall block of every turn (chat and agent/coder);
 *     memory_store(messages[{role,content}], session) ← every turn's new
 *       messages, forwarded asynchronously and best-effort.
 *
 *   CHATCLI_CONTEXT_ENGINE=mcp:<server>
 *     context_compact(segment, budget_chars, instruction) → the summary
 *       that replaces the compacted segment (auto-compact and guided
 *       /compact); any failure falls back to the embedded summarizer.
 *
 * Every external call is bounded and every failure degrades to the
 * embedded behavior, so a misconfigured or slow server can never stall
 * or break a turn.
 */
package cli

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/cli/mcp"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

const (
	// MemoryProviderEnv selects the memory provider: builtin (default)
	// or mcp:<server>.
	MemoryProviderEnv = "CHATCLI_MEMORY_PROVIDER"
	// ContextEngineEnv selects the context engine: builtin (default) or
	// mcp:<server>.
	ContextEngineEnv = "CHATCLI_CONTEXT_ENGINE"

	extMemoryRecallTool   = "memory_recall"
	extMemoryStoreTool    = "memory_store"
	extContextCompactTool = "context_compact"

	extRecallTimeout  = 5 * time.Second
	extStoreTimeout   = 10 * time.Second
	extCompactTimeout = 3 * time.Minute
	// extRecallBudget bounds what an external provider may add to the
	// auto-recall block.
	extRecallBudget = 900
)

// extensionTarget parses "mcp:<server>" (case-insensitive scheme); ok is
// false for builtin, empty or malformed values.
func extensionTarget(raw string) (server string, ok bool) {
	v := strings.TrimSpace(raw)
	if v == "" || strings.EqualFold(v, "builtin") {
		return "", false
	}
	if !strings.HasPrefix(strings.ToLower(v), "mcp:") {
		return "", false
	}
	server = strings.TrimSpace(v[4:])
	return server, server != ""
}

// memoryProviderServer / contextEngineServer read the env selectors.
func memoryProviderServer() (string, bool) { return extensionTarget(os.Getenv(MemoryProviderEnv)) }
func contextEngineServer() (string, bool)  { return extensionTarget(os.Getenv(ContextEngineEnv)) }

// serverToolCaller is what the extension points need from the MCP
// manager (satisfied by *mcp.Manager; stubbed in tests).
type serverToolCaller interface {
	ExecuteServerTool(ctx context.Context, server, tool string, args map[string]interface{}) (*mcp.MCPToolResult, error)
}

// extCaller returns the tool caller (nil when MCP is off).
func (cli *ChatCLI) extCaller() serverToolCaller {
	if cli == nil {
		return nil
	}
	if cli.extToolCaller != nil {
		return cli.extToolCaller
	}
	if cli.mcpManager == nil {
		return nil
	}
	return cli.mcpManager
}

// errExtUnavailable marks a call that could not be made (no MCP, no server).
var errExtUnavailable = errors.New("extension provider unavailable")

func (cli *ChatCLI) callExtTool(ctx context.Context, server, tool string, args map[string]interface{}, timeout time.Duration) (string, error) {
	caller := cli.extCaller()
	if caller == nil {
		return "", errExtUnavailable
	}
	opCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	res, err := caller.ExecuteServerTool(opCtx, server, tool, args)
	if err != nil {
		return "", err
	}
	if res == nil {
		return "", nil
	}
	if res.IsError {
		return "", errors.New(strings.TrimSpace(res.Content))
	}
	return strings.TrimSpace(res.Content), nil
}

// externalMemoryRecall asks the configured provider for what it knows
// about the turn. Empty when no provider is configured or it fails.
func (cli *ChatCLI) externalMemoryRecall(ctx context.Context, query string, hints []string) string {
	server, ok := memoryProviderServer()
	if !ok {
		return ""
	}
	text, err := cli.callExtTool(ctx, server, extMemoryRecallTool, map[string]interface{}{
		"query":        query,
		"hints":        hints,
		"budget_chars": extRecallBudget,
	}, extRecallTimeout)
	if err != nil {
		if cli.logger != nil && !errors.Is(err, errExtUnavailable) {
			cli.logger.Debug("external memory recall failed; embedded recall only", zap.String("server", server), zap.Error(err))
		}
		return ""
	}
	if len(text) > extRecallBudget {
		text = text[:extRecallBudget] + "…"
	}
	return text
}

// externalMemoryStore forwards new messages to the provider (best-effort,
// asynchronous). session names the conversation for the provider's own
// bookkeeping.
func (cli *ChatCLI) externalMemoryStore(ctx context.Context, session string, msgs []models.Message) {
	server, ok := memoryProviderServer()
	if !ok || len(msgs) == 0 || cli.extCaller() == nil {
		return
	}
	payload := make([]map[string]string, 0, len(msgs))
	for _, m := range msgs {
		if strings.TrimSpace(m.Content) == "" {
			continue
		}
		payload = append(payload, map[string]string{"role": m.Role, "content": m.Content})
	}
	if len(payload) == 0 {
		return
	}
	detached := context.WithoutCancel(ctx)
	go func() {
		if _, err := cli.callExtTool(detached, server, extMemoryStoreTool, map[string]interface{}{
			"messages": payload,
			"session":  session,
		}, extStoreTimeout); err != nil && cli.logger != nil && !errors.Is(err, errExtUnavailable) {
			cli.logger.Debug("external memory store failed", zap.String("server", server), zap.Error(err))
		}
	}()
}

// ExternalSummarizer is the context-engine seam: given the rendered
// segment, the character budget and an optional user instruction, return
// the summary that replaces the segment.
type ExternalSummarizer func(ctx context.Context, segment string, budgetChars int, instruction string) (string, error)

// externalSummarizer returns the context engine's compact function when
// one is configured, nil otherwise. The compactor calls it with the
// rendered segment and its character budget and falls back to the
// embedded summarizer on error or empty output.
func (cli *ChatCLI) externalSummarizer() ExternalSummarizer {
	server, ok := contextEngineServer()
	if !ok || cli == nil || cli.extCaller() == nil {
		return nil
	}
	return func(ctx context.Context, segment string, budgetChars int, instruction string) (string, error) {
		out, err := cli.callExtTool(ctx, server, extContextCompactTool, map[string]interface{}{
			"segment":      segment,
			"budget_chars": budgetChars,
			"instruction":  instruction,
		}, extCompactTimeout)
		if err != nil {
			return "", err
		}
		if out == "" {
			return "", errors.New("context engine returned an empty summary")
		}
		return out, nil
	}
}

// extForwardState tracks what the memory worker already forwarded to the
// external provider from the live history.
type extForwardState struct {
	mu        sync.Mutex
	forwarded int
}

// forwardNewHistory forwards history[forwarded:] to the provider and
// advances the mark; a shrunk history (compaction, /clear) resets it.
func (cli *ChatCLI) forwardNewHistory(ctx context.Context, st *extForwardState, history []models.Message, session string) {
	if _, ok := memoryProviderServer(); !ok || st == nil {
		return
	}
	st.mu.Lock()
	if st.forwarded > len(history) {
		st.forwarded = 0
	}
	start := st.forwarded
	st.forwarded = len(history)
	st.mu.Unlock()
	if start >= len(history) {
		return
	}
	fresh := make([]models.Message, 0, len(history)-start)
	for _, m := range history[start:] {
		if strings.EqualFold(m.Role, "system") {
			continue
		}
		fresh = append(fresh, m)
	}
	cli.externalMemoryStore(ctx, session, fresh)
}

// extensionStatus renders the configured extension points for /config.
func extensionStatus(raw string) string {
	if server, ok := extensionTarget(raw); ok {
		return "mcp:" + server
	}
	return "builtin"
}
