/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * Agent-run mirroring: the optional hub capability behind cross-process
 * /agents. Each chatcli process (REPL, gateway daemon, scheduler daemon)
 * upserts snapshots of its live agent runs into the agent_runs table; any
 * other process can list them and render "what is running elsewhere".
 *
 * Unlike conversation events, run rows are UPSERTED, not appended: a run
 * emits many progress updates per turn and only its latest state matters,
 * so an append-only log would grow without bound for zero benefit. The
 * updated_at column doubles as a liveness heartbeat — writers refresh it
 * on every sync tick, and readers treat a "running" row whose heartbeat
 * stopped as stale (its process likely died without finalizing).
 *
 * Cross-process cancellation rides the same table: any process may flag
 * cancel_requested on a row; the owning process observes the flag on its
 * next sync tick and cancels the run locally (only the owner holds the
 * context.CancelFunc).
 */
package hub

import (
	"context"
	"time"
)

// AgentRunRecord is one mirrored agent run as stored in the hub. Payload
// carries the owning process's JSON snapshot verbatim (the cli run wire
// projection) so the hub package stays decoupled from cli types; the
// remaining columns are the query surface.
type AgentRunRecord struct {
	RunID           string
	Instance        string // owning-process token (namespaces run IDs)
	Origin          string // "repl", "gateway", "scheduler", "mcp", "acp"
	Status          string // "running", "completed", "failed", "cancelled"
	Payload         string // JSON snapshot of the run
	CancelRequested bool
	UpdatedAt       time.Time // last upsert — doubles as liveness heartbeat
	EndedAt         time.Time // zero while running
}

// AgentRunStore is the optional capability implemented by hub stores that
// can mirror agent runs. It is deliberately NOT part of Store: extending
// that exported interface would break external implementations, so callers
// type-assert (store.(AgentRunStore)) and degrade gracefully when absent.
type AgentRunStore interface {
	// UpsertAgentRun inserts or refreshes one run snapshot, preserving any
	// pending cancel_requested flag set by another process.
	UpsertAgentRun(ctx context.Context, rec AgentRunRecord) error
	// ListAgentRuns returns all mirrored runs, most recently updated first.
	ListAgentRuns(ctx context.Context) ([]AgentRunRecord, error)
	// RequestAgentRunCancel flags a mirrored run for cancellation by its
	// owning process. Returns false when the run is unknown or already
	// terminal.
	RequestAgentRunCancel(ctx context.Context, runID string) (bool, error)
	// PurgeAgentRuns deletes terminal rows not updated within
	// terminalOlderThan and running rows whose heartbeat stopped for longer
	// than runningOlderThan (their process died without finalizing).
	// Returns the number of rows removed.
	PurgeAgentRuns(ctx context.Context, terminalOlderThan, runningOlderThan time.Duration) (int64, error)
}

var _ AgentRunStore = (*SQLiteStore)(nil)

// terminalAgentRunStatuses mirrors runs.Status.Terminal without importing
// the cli package.
const terminalAgentRunStatusesSQL = "('completed','failed','cancelled')"

// UpsertAgentRun implements AgentRunStore.
func (s *SQLiteStore) UpsertAgentRun(ctx context.Context, rec AgentRunRecord) error {
	if rec.RunID == "" || rec.Instance == "" {
		return nil
	}
	if rec.UpdatedAt.IsZero() {
		rec.UpdatedAt = time.Now()
	}
	var endedAt int64
	if !rec.EndedAt.IsZero() {
		endedAt = rec.EndedAt.UnixMilli()
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO agent_runs (run_id, instance, origin, status, payload, cancel_requested, updated_at, ended_at)
		VALUES (?, ?, ?, ?, ?, 0, ?, ?)
		ON CONFLICT(run_id) DO UPDATE SET
			status     = excluded.status,
			payload    = excluded.payload,
			updated_at = excluded.updated_at,
			ended_at   = excluded.ended_at`,
		rec.RunID, rec.Instance, rec.Origin, rec.Status, rec.Payload,
		rec.UpdatedAt.UnixMilli(), endedAt)
	return err
}

// ListAgentRuns implements AgentRunStore.
func (s *SQLiteStore) ListAgentRuns(ctx context.Context) ([]AgentRunRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT run_id, instance, origin, status, payload, cancel_requested, updated_at, ended_at
		FROM agent_runs ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []AgentRunRecord
	for rows.Next() {
		var rec AgentRunRecord
		var cancelRequested int
		var updatedAt, endedAt int64
		if err := rows.Scan(&rec.RunID, &rec.Instance, &rec.Origin, &rec.Status,
			&rec.Payload, &cancelRequested, &updatedAt, &endedAt); err != nil {
			return nil, err
		}
		rec.CancelRequested = cancelRequested != 0
		rec.UpdatedAt = time.UnixMilli(updatedAt)
		if endedAt > 0 {
			rec.EndedAt = time.UnixMilli(endedAt)
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// RequestAgentRunCancel implements AgentRunStore.
func (s *SQLiteStore) RequestAgentRunCancel(ctx context.Context, runID string) (bool, error) {
	if runID == "" {
		return false, nil
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	res, err := s.db.ExecContext(ctx, `
		UPDATE agent_runs SET cancel_requested = 1
		WHERE run_id = ? AND status NOT IN `+terminalAgentRunStatusesSQL,
		runID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// PurgeAgentRuns implements AgentRunStore.
func (s *SQLiteStore) PurgeAgentRuns(ctx context.Context, terminalOlderThan, runningOlderThan time.Duration) (int64, error) {
	now := time.Now()
	s.wmu.Lock()
	defer s.wmu.Unlock()
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM agent_runs
		WHERE (status IN `+terminalAgentRunStatusesSQL+` AND updated_at < ?)
		   OR (status NOT IN `+terminalAgentRunStatusesSQL+` AND updated_at < ?)`,
		now.Add(-terminalOlderThan).UnixMilli(),
		now.Add(-runningOlderThan).UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
