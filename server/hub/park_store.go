/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * Park mirroring: the optional hub capability behind cross-process
 * @park cancel. A parked agent's snapshot lives on disk and any process can
 * load it, but only the process that parked it holds the scheduler job and
 * the wake path (REPL queue or in-turn waiter), so a cancel issued elsewhere
 * must reach that process.
 *
 * Each process upserts one row per park it owns, refreshing heartbeat_at on
 * every sync tick; a row whose heartbeat stopped means the owner died and
 * the park may be resumed by whoever cancels it. A cancel from another
 * process writes the wake request onto the row; the owner observes it on its
 * next tick, cancels its scheduler job, wakes the agent with that outcome and
 * deletes the row.
 */
package hub

import (
	"context"
	"time"
)

// ParkMirrorRecord is one parked agent as mirrored in the hub.
type ParkMirrorRecord struct {
	Token           string
	Instance        string    // owning-process token
	Origin          string    // "repl", "gateway", …
	HeartbeatAt     time.Time // last refresh by the owner
	WakeOutcome     string    // non-empty once another process asked for a wake
	WakeDetail      string
	WakeRequestedAt time.Time // zero while no wake is pending
}

// ParkMirrorStore is the optional capability implemented by hub stores that
// can mirror parks. Like AgentRunStore it is NOT part of Store: callers
// type-assert and degrade gracefully when absent.
type ParkMirrorStore interface {
	// UpsertParkOwner inserts or refreshes the owner row of token (the
	// heartbeat), preserving a pending wake request.
	UpsertParkOwner(ctx context.Context, rec ParkMirrorRecord) error
	// ListParkMirror returns every mirrored park, most recent heartbeat first.
	ListParkMirror(ctx context.Context) ([]ParkMirrorRecord, error)
	// RequestParkWake records a wake request for token. Returns false when
	// no process mirrors that token.
	RequestParkWake(ctx context.Context, token, outcome, detail string) (bool, error)
	// DeleteParkMirror removes token's row (the owner resumed or dropped it).
	DeleteParkMirror(ctx context.Context, token string) error
	// PurgeParkMirror deletes rows whose heartbeat is older than olderThan
	// (their process died). Returns the number of rows removed.
	PurgeParkMirror(ctx context.Context, olderThan time.Duration) (int64, error)
}

var _ ParkMirrorStore = (*SQLiteStore)(nil)

// UpsertParkOwner implements ParkMirrorStore.
func (s *SQLiteStore) UpsertParkOwner(ctx context.Context, rec ParkMirrorRecord) error {
	if rec.Token == "" || rec.Instance == "" {
		return nil
	}
	if rec.HeartbeatAt.IsZero() {
		rec.HeartbeatAt = time.Now()
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO park_mirror (token, instance, origin, heartbeat_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(token) DO UPDATE SET
			instance     = excluded.instance,
			origin       = excluded.origin,
			heartbeat_at = excluded.heartbeat_at`,
		rec.Token, rec.Instance, rec.Origin, rec.HeartbeatAt.UnixMilli())
	return err
}

// ListParkMirror implements ParkMirrorStore.
func (s *SQLiteStore) ListParkMirror(ctx context.Context) ([]ParkMirrorRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT token, instance, origin, heartbeat_at, wake_outcome, wake_detail, wake_requested_at
		FROM park_mirror ORDER BY heartbeat_at DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []ParkMirrorRecord
	for rows.Next() {
		var rec ParkMirrorRecord
		var heartbeat, requested int64
		if err := rows.Scan(&rec.Token, &rec.Instance, &rec.Origin, &heartbeat,
			&rec.WakeOutcome, &rec.WakeDetail, &requested); err != nil {
			return nil, err
		}
		rec.HeartbeatAt = time.UnixMilli(heartbeat)
		if requested > 0 {
			rec.WakeRequestedAt = time.UnixMilli(requested)
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// RequestParkWake implements ParkMirrorStore.
func (s *SQLiteStore) RequestParkWake(ctx context.Context, token, outcome, detail string) (bool, error) {
	if token == "" || outcome == "" {
		return false, nil
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	res, err := s.db.ExecContext(ctx, `
		UPDATE park_mirror SET wake_outcome = ?, wake_detail = ?, wake_requested_at = ?
		WHERE token = ?`,
		outcome, detail, time.Now().UnixMilli(), token)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// DeleteParkMirror implements ParkMirrorStore.
func (s *SQLiteStore) DeleteParkMirror(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM park_mirror WHERE token = ?`, token)
	return err
}

// PurgeParkMirror implements ParkMirrorStore.
func (s *SQLiteStore) PurgeParkMirror(ctx context.Context, olderThan time.Duration) (int64, error) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	res, err := s.db.ExecContext(ctx, `DELETE FROM park_mirror WHERE heartbeat_at < ?`,
		time.Now().Add(-olderThan).UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
