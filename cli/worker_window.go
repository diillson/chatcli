/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The orchestrator's window management, shared with squad, taskgraph and
 * delegate workers: the session compactor (with the active tenant's CCR)
 * and the transcript journal. Workers used to have nothing beyond the L0
 * microcompact — no compaction, no journal, no overflow recovery.
 */
package cli

import (
	"context"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// workerWindow implements workers.WindowManager over the live session.
type workerWindow struct{ cli *ChatCLI }

// NeedsCompaction applies the session's compaction budget to a worker
// history (the worker's own system prompt counts as reserved prefix).
func (w *workerWindow) NeedsCompaction(history []models.Message) bool {
	if w == nil || w.cli == nil || w.cli.historyCompactor == nil {
		return false
	}
	return w.cli.historyCompactor.NeedsCompaction(history, w.cli.compactConfig(w.cli.Provider, w.cli.Model))
}

// Compact runs the session compactor (Level 1 trim, Level 2 summary with
// the configured summarizer, CCR archive through the tenant's layer).
func (w *workerWindow) Compact(ctx context.Context, history []models.Message) ([]models.Message, error) {
	if w == nil || w.cli == nil || w.cli.historyCompactor == nil {
		return history, nil
	}
	cfg := w.cli.compactConfig(w.cli.Provider, w.cli.Model)
	out, err := w.cli.historyCompactor.Compact(ctx, history, w.cli.getClient(), cfg)
	if err != nil {
		return history, err
	}
	if w.cli.costTracker != nil {
		w.cli.costTracker.NoteExpectedCacheRebuild()
	}
	return out, nil
}

// NoteTurn journals the worker's turn under the parent session.
func (w *workerWindow) NoteTurn(worker string, turn []models.Message) {
	if w == nil || w.cli == nil || w.cli.transcript == nil {
		return
	}
	if err := w.cli.transcript.appendWorkerTurn(worker, turn); err != nil && w.cli.logger != nil {
		w.cli.logger.Debug("worker turn not journaled", zap.String("worker", worker), zap.Error(err))
	}
}
