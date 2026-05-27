/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package hub

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	_ "modernc.org/sqlite" // pure-Go driver, registers as "sqlite" (no CGO — keeps cross-compilation, incl. Windows, working)

	"github.com/diillson/chatcli/models"
)

// schema is the embedded-DB layout. The events table uses a global
// AUTOINCREMENT rowid as Seq: it is strictly increasing per conversation
// (filtered by conv_id), which is all the tail/resume logic needs, and avoids
// a per-conversation counter race. The partial unique index enforces
// idempotency only when a client_msg_id is supplied.
const schema = `
CREATE TABLE IF NOT EXISTS conversations (
    conv_id    TEXT PRIMARY KEY,
    principal  TEXT NOT NULL,
    created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_conversations_principal ON conversations(principal);

CREATE TABLE IF NOT EXISTS events (
    seq           INTEGER PRIMARY KEY AUTOINCREMENT,
    conv_id       TEXT NOT NULL,
    principal     TEXT NOT NULL,
    channel       TEXT NOT NULL,
    role          TEXT NOT NULL,
    content       TEXT NOT NULL,
    client_msg_id TEXT NOT NULL DEFAULT '',
    ts            INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_events_conv_seq ON events(conv_id, seq);
CREATE UNIQUE INDEX IF NOT EXISTS idx_events_dedupe ON events(conv_id, client_msg_id) WHERE client_msg_id <> '';

CREATE TABLE IF NOT EXISTS pointers (
    principal      TEXT PRIMARY KEY,
    active_conv_id TEXT NOT NULL,
    updated_at     INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS bindings (
    platform   TEXT NOT NULL,
    user_id    TEXT NOT NULL,
    principal  TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (platform, user_id)
);

CREATE TABLE IF NOT EXISTS settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
`

// SQLiteStore is the WAL-backed implementation of Store. It is safe for
// concurrent use: reads go through the connection pool, writes are serialized
// by wmu so the embedded single-writer never returns SQLITE_BUSY to callers.
type SQLiteStore struct {
	db     *sql.DB
	logger *zap.Logger
	wmu    sync.Mutex // serializes writes
}

// OpenSQLiteStore opens (creating if needed) the Hub database at path with WAL
// journaling and runs migrations. A nil logger is replaced with a no-op.
func OpenSQLiteStore(ctx context.Context, path string, logger *zap.Logger) (*SQLiteStore, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	// WAL gives concurrent readers alongside one writer; busy_timeout absorbs
	// brief contention without surfacing errors.
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("hub: open sqlite at %s: %w", path, err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("hub: ping sqlite at %s: %w", path, err)
	}
	if _, err := db.ExecContext(ctx, schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("hub: migrate schema: %w", err)
	}
	return &SQLiteStore{db: db, logger: logger}, nil
}

// Close releases the underlying database.
func (s *SQLiteStore) Close() error { return s.db.Close() }

// Resolve returns the active conversation for a principal, creating one on
// first contact. The read fast-path needs no lock; creation is safe across
// processes sharing the database (the local CLI and the gateway daemon), since
// the write lock only serializes within one process — see resolveCreateLocked.
func (s *SQLiteStore) Resolve(ctx context.Context, principal string) (string, error) {
	if principal == "" {
		return "", errors.New("hub: empty principal")
	}
	if convID, err := s.activePointer(ctx, principal); err == nil {
		return convID, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}

	s.wmu.Lock()
	defer s.wmu.Unlock()
	return s.resolveCreateLocked(ctx, principal)
}

// resolveCreateLocked lazily creates the principal's first conversation. The
// pointer insert uses ON CONFLICT DO NOTHING so that if another process created
// the pointer first, we adopt theirs instead of forking a second conversation —
// the pointer row is the single, atomically-enforced source of truth. Caller
// must hold wmu.
func (s *SQLiteStore) resolveCreateLocked(ctx context.Context, principal string) (string, error) {
	convID := uuid.New().String()
	now := time.Now().UTC().UnixMilli()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("hub: begin resolve: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx,
		`INSERT INTO pointers(principal, active_conv_id, updated_at) VALUES(?,?,?)
         ON CONFLICT(principal) DO NOTHING`, principal, convID, now)
	if err != nil {
		return "", fmt.Errorf("hub: insert pointer: %w", err)
	}
	won, err := res.RowsAffected()
	if err != nil {
		return "", fmt.Errorf("hub: pointer rows affected: %w", err)
	}
	if won == 1 {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO conversations(conv_id, principal, created_at) VALUES(?,?,?)`,
			convID, principal, now); err != nil {
			return "", fmt.Errorf("hub: insert conversation: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("hub: commit resolve: %w", err)
	}
	if won == 1 {
		return convID, nil
	}
	// Another process won the race (or the pointer already existed): adopt it.
	return s.activePointer(ctx, principal)
}

// NewConversation always rotates the pointer to a fresh conversation.
func (s *SQLiteStore) NewConversation(ctx context.Context, principal string) (string, error) {
	if principal == "" {
		return "", errors.New("hub: empty principal")
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return s.createConversationLocked(ctx, principal)
}

// createConversationLocked rotates the principal to a fresh conversation and
// prunes the one it replaces, so the hub stays a bounded cross-channel buffer
// rather than a growing archive (long-term recall is the memory system's job).
// Caller must hold wmu.
func (s *SQLiteStore) createConversationLocked(ctx context.Context, principal string) (string, error) {
	prior, _ := s.activePointer(ctx, principal) // "" on first contact
	convID := uuid.New().String()
	now := time.Now().UTC().UnixMilli()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("hub: begin new conversation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO conversations(conv_id, principal, created_at) VALUES(?,?,?)`,
		convID, principal, now); err != nil {
		return "", fmt.Errorf("hub: insert conversation: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO pointers(principal, active_conv_id, updated_at) VALUES(?,?,?)
         ON CONFLICT(principal) DO UPDATE SET active_conv_id=excluded.active_conv_id, updated_at=excluded.updated_at`,
		principal, convID, now); err != nil {
		return "", fmt.Errorf("hub: update pointer: %w", err)
	}
	if prior != "" && prior != convID {
		if _, err := tx.ExecContext(ctx, `DELETE FROM events WHERE conv_id = ?`, prior); err != nil {
			return "", fmt.Errorf("hub: prune prior events: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM conversations WHERE conv_id = ?`, prior); err != nil {
			return "", fmt.Errorf("hub: prune prior conversation: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("hub: commit new conversation: %w", err)
	}
	return convID, nil
}

// PurgeIdle deletes conversations whose most recent activity is older than
// olderThan (and their events), reclaiming space from abandoned threads. A
// conversation with no events falls back to its creation time. Returns the
// number of conversations removed.
func (s *SQLiteStore) PurgeIdle(ctx context.Context, olderThan time.Duration) (int, error) {
	if olderThan <= 0 {
		return 0, nil
	}
	cutoff := time.Now().Add(-olderThan).UTC().UnixMilli()

	s.wmu.Lock()
	defer s.wmu.Unlock()

	// Never purge a principal's currently-active conversation.
	const q = `
SELECT c.conv_id FROM conversations c
WHERE c.conv_id NOT IN (SELECT active_conv_id FROM pointers)
  AND COALESCE((SELECT MAX(ts) FROM events e WHERE e.conv_id = c.conv_id), c.created_at) < ?`
	rows, err := s.db.QueryContext(ctx, q, cutoff)
	if err != nil {
		return 0, fmt.Errorf("hub: scan idle conversations: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("hub: scan idle id: %w", err)
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	for _, id := range ids {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM events WHERE conv_id = ?`, id); err != nil {
			return 0, fmt.Errorf("hub: purge idle events: %w", err)
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM conversations WHERE conv_id = ?`, id); err != nil {
			return 0, fmt.Errorf("hub: purge idle conversation: %w", err)
		}
	}
	return len(ids), nil
}

func (s *SQLiteStore) activePointer(ctx context.Context, principal string) (string, error) {
	var convID string
	err := s.db.QueryRowContext(ctx,
		`SELECT active_conv_id FROM pointers WHERE principal = ?`, principal).Scan(&convID)
	return convID, err
}

// Append writes an event, assigning its Seq. With a ClientMsgID it is
// idempotent: a repeat returns the previously stored event.
func (s *SQLiteStore) Append(ctx context.Context, ev models.ConversationEvent) (models.ConversationEvent, error) {
	if ev.ConvID == "" {
		return ev, errors.New("hub: append requires ConvID")
	}
	if ev.Role == "" {
		return ev, errors.New("hub: append requires Role")
	}
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}

	s.wmu.Lock()
	defer s.wmu.Unlock()

	if ev.ClientMsgID != "" {
		if existing, ok, err := s.lookupByClientMsgID(ctx, ev.ConvID, ev.ClientMsgID); err != nil {
			return ev, err
		} else if ok {
			return existing, nil
		}
	}

	res, err := s.db.ExecContext(ctx,
		`INSERT INTO events(conv_id, principal, channel, role, content, client_msg_id, ts)
         VALUES(?,?,?,?,?,?,?)`,
		ev.ConvID, ev.Principal, ev.Channel, ev.Role, ev.Content, ev.ClientMsgID, ev.Timestamp.UnixMilli())
	if err != nil {
		return ev, fmt.Errorf("hub: append event: %w", err)
	}
	seq, err := res.LastInsertId()
	if err != nil {
		return ev, fmt.Errorf("hub: append seq: %w", err)
	}
	ev.Seq = seq
	return ev, nil
}

func (s *SQLiteStore) lookupByClientMsgID(ctx context.Context, convID, clientMsgID string) (models.ConversationEvent, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT seq, conv_id, principal, channel, role, content, client_msg_id, ts
         FROM events WHERE conv_id = ? AND client_msg_id = ?`, convID, clientMsgID)
	ev, err := scanEvent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return models.ConversationEvent{}, false, nil
	}
	if err != nil {
		return models.ConversationEvent{}, false, fmt.Errorf("hub: dedupe lookup: %w", err)
	}
	return ev, true, nil
}

// Read returns events with Seq > sinceSeq, ordered ascending.
func (s *SQLiteStore) Read(ctx context.Context, convID string, sinceSeq int64, limit int) ([]models.ConversationEvent, error) {
	query := `SELECT seq, conv_id, principal, channel, role, content, client_msg_id, ts
              FROM events WHERE conv_id = ? AND seq > ? ORDER BY seq ASC`
	args := []any{convID, sinceSeq}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("hub: read events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []models.ConversationEvent
	for rows.Next() {
		ev, err := scanEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("hub: scan event: %w", err)
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("hub: iterate events: %w", err)
	}
	return out, nil
}

// ResolvePrincipal maps a channel identity to its principal.
func (s *SQLiteStore) ResolvePrincipal(ctx context.Context, platform, userID string) (string, error) {
	var principal string
	err := s.db.QueryRowContext(ctx,
		`SELECT principal FROM bindings WHERE platform = ? AND user_id = ?`, platform, userID).Scan(&principal)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrUnboundChannel
	}
	if err != nil {
		return "", fmt.Errorf("hub: resolve principal: %w", err)
	}
	return principal, nil
}

// Bind upserts a channel-identity → principal binding.
func (s *SQLiteStore) Bind(ctx context.Context, platform, userID, principal string) error {
	if platform == "" || userID == "" || principal == "" {
		return errors.New("hub: bind requires platform, userID and principal")
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO bindings(platform, user_id, principal, created_at) VALUES(?,?,?,?)
         ON CONFLICT(platform, user_id) DO UPDATE SET principal=excluded.principal`,
		platform, userID, principal, time.Now().UTC().UnixMilli())
	if err != nil {
		return fmt.Errorf("hub: bind: %w", err)
	}
	return nil
}

// ListBindings returns bindings, optionally filtered to one principal.
func (s *SQLiteStore) ListBindings(ctx context.Context, principal string) ([]Binding, error) {
	query := `SELECT platform, user_id, principal FROM bindings`
	var args []any
	if principal != "" {
		query += " WHERE principal = ?"
		args = append(args, principal)
	}
	query += " ORDER BY platform, user_id"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("hub: list bindings: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Binding
	for rows.Next() {
		var b Binding
		if err := rows.Scan(&b.Platform, &b.UserID, &b.Principal); err != nil {
			return nil, fmt.Errorf("hub: scan binding: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// OwnerOf returns the principal owning a conversation.
func (s *SQLiteStore) OwnerOf(ctx context.Context, convID string) (string, error) {
	var principal string
	err := s.db.QueryRowContext(ctx,
		`SELECT principal FROM conversations WHERE conv_id = ?`, convID).Scan(&principal)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("hub: conversation %q not found", convID)
	}
	if err != nil {
		return "", fmt.Errorf("hub: owner of: %w", err)
	}
	return principal, nil
}

// GetSetting returns a runtime setting and whether it was present. Settings live
// in the shared database, so a value set by the CLI is read live by the gateway
// (and vice versa) without an env var or a restart.
func (s *SQLiteStore) GetSetting(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("hub: get setting %q: %w", key, err)
	}
	return v, true, nil
}

// SetSetting upserts a runtime setting.
func (s *SQLiteStore) SetSetting(ctx context.Context, key, value string) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings(key, value) VALUES(?,?)
         ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("hub: set setting %q: %w", key, err)
	}
	return nil
}

// DeleteSetting removes a runtime setting so resolution falls back to env/default.
func (s *SQLiteStore) DeleteSetting(ctx context.Context, key string) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key); err != nil {
		return fmt.Errorf("hub: delete setting %q: %w", key, err)
	}
	return nil
}

// AllSettings returns every stored runtime setting.
func (s *SQLiteStore) AllSettings(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return nil, fmt.Errorf("hub: list settings: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("hub: scan setting: %w", err)
		}
		out[k] = v
	}
	return out, rows.Err()
}

// scanner abstracts *sql.Row and *sql.Rows for scanEvent.
type scanner interface {
	Scan(dest ...any) error
}

func scanEvent(s scanner) (models.ConversationEvent, error) {
	var (
		ev    models.ConversationEvent
		tsMil int64
	)
	if err := s.Scan(&ev.Seq, &ev.ConvID, &ev.Principal, &ev.Channel, &ev.Role, &ev.Content, &ev.ClientMsgID, &tsMil); err != nil {
		return models.ConversationEvent{}, err
	}
	ev.Timestamp = time.UnixMilli(tsMil).UTC()
	return ev, nil
}

var _ Store = (*SQLiteStore)(nil)
