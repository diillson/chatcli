/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Context-overflow recovery for the non-agent loops.
 *
 * The agent/coder loop has recovered from "context too long" and
 * proxy-payload rejections for a long time (aggressive budget → level 2 →
 * emergency truncation, then retry). The chat REPL, RPC chat (MCP, ACP,
 * gateway), one-shot and MoA participants simply failed the turn. They
 * now share one bounded helper with the same guarantees a planned
 * compaction gives: memory flushed first, hooks told, dropped messages
 * archived to CCR, the cache rebuild accounted for.
 */
package cli

import (
	"context"
	"errors"

	"github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// overflowRecovery bounds recovery attempts for one turn on one surface.
type overflowRecovery struct {
	cr      *agent.ContextRecovery
	surface string
	notify  func(string)
}

// newOverflowRecovery starts a per-turn recovery budget. notify renders a
// one-line notice to the user (nil = silent, e.g. RPC surfaces).
func (cli *ChatCLI) newOverflowRecovery(surface string, notify func(string)) *overflowRecovery {
	logger := zap.NewNop()
	if cli != nil && cli.logger != nil {
		logger = cli.logger
	}
	return &overflowRecovery{cr: agent.NewContextRecovery(agent.DefaultContextRecoveryConfig(), logger), surface: surface, notify: notify}
}

// overflowLike reports whether err is a context overflow or a payload
// rejection worth recovering from given the history size.
func overflowLike(err error, history []models.Message) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if agent.IsContextTooLongError(err) {
		return true
	}
	chars := 0
	for _, m := range history {
		chars += len(m.Content)
	}
	return agent.IsLikelyPayloadProblem(err, chars)
}

// recover compacts cli.history in place after an overflow error and
// reports whether the caller should rebuild its request and retry. It
// never retries past the configured attempts and never touches the
// history for any other error.
func (cli *ChatCLI) recoverOverflow(ctx context.Context, rec *overflowRecovery, err error) bool {
	if cli == nil || rec == nil || !overflowLike(err, cli.history) || !rec.cr.CanRecoverContextOverflow() {
		return false
	}
	cli.flushMemoryBeforeCompaction(ctx)
	cli.beforeCompaction(ctx, compactTriggerRecovery)
	recovered, ok := rec.cr.RecoverContextOverflow(cli.history)
	if !ok {
		return false
	}
	if note := archiveDroppedMessages(cli.compressionLayer, cli.history, recovered); note != "" {
		recovered = append(recovered, models.Message{Role: "user", Content: note})
	}
	cli.history = recovered
	if cli.costTracker != nil {
		cli.costTracker.NoteExpectedCacheRebuild()
	}
	cli.firePostCompact(ctx, compactTriggerRecovery)
	if cli.logger != nil {
		cli.logger.Warn("Context overflow recovered; retrying the turn",
			zap.String("surface", rec.surface), zap.Int("history", len(cli.history)), zap.Error(err))
	}
	if rec.notify != nil {
		rec.notify(i18n.T("chat.recovery.retrying", rec.surface))
	}
	return true
}

// recoverOverflowHistory is the standalone-history variant for callers
// whose conversation is not cli.history (a MoA participant's own thread):
// no hooks, no CCR — the thread is transient — just the bounded compaction
// and the retry decision.
func (rec *overflowRecovery) recoverHistory(err error, history []models.Message) ([]models.Message, bool) {
	if rec == nil || !overflowLike(err, history) || !rec.cr.CanRecoverContextOverflow() {
		return history, false
	}
	return rec.cr.RecoverContextOverflow(history)
}
