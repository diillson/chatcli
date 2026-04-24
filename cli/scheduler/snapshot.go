/*
 * ChatCLI - Scheduler: periodic state snapshot.
 *
 * Rationale: the WAL alone recovers correctly, but boot time grows
 * linearly with total record count. A periodic snapshot freezes the
 * live state to a single file (snapshot.json). On boot the scheduler
 * loads the snapshot first, then overlays any .wal records newer than
 * the snapshot's timestamp. Records covered by the snapshot can be
 * GC'd out of the WAL directory.
 *
 * The snapshot is written atomically via tmp-rename so partial writes
 * are never observable. Stale / corrupt snapshots are ignored in favor
 * of a full WAL scan — the WAL is the truth, the snapshot is cache.
 */
package scheduler

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"go.uber.org/zap"
)

// snapshotEnvelope is the on-disk format.
type snapshotEnvelope struct {
	Version    int       `json:"v"`
	CapturedAt time.Time `json:"captured_at"`
	Jobs       []*Job    `json:"jobs"`
}

// writeSnapshot persists the current scheduler state to
// <dir>/snapshot.json atomically.
func writeSnapshot(dir string, jobs []*Job, logger *zap.Logger) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("scheduler snapshot: mkdir: %w", err)
	}
	// Sort for deterministic diffs (nice for operators).
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].CreatedAt.Before(jobs[j].CreatedAt) })

	env := snapshotEnvelope{
		Version:    SchemaVersion,
		CapturedAt: time.Now().UTC(),
		Jobs:       jobs,
	}
	payload, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return fmt.Errorf("scheduler snapshot: marshal: %w", err)
	}
	finalPath := filepath.Join(dir, "snapshot.json")
	tmpPath := filepath.Join(dir, "snapshot.tmp")

	if err := writeAndSyncWAL(tmpPath, payload); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("scheduler snapshot: write tmp: %w", err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("scheduler snapshot: rename: %w", err)
	}
	if err := syncWALDir(dir); err != nil && logger != nil {
		logger.Warn("scheduler snapshot: dir fsync failed", zap.Error(err))
	}
	return nil
}

// readSnapshot loads a previously written snapshot, if any. Missing /
// corrupt snapshots return (nil, nil) — the caller falls back to a
// full WAL scan.
func readSnapshot(dir string, logger *zap.Logger) (*snapshotEnvelope, error) {
	path := filepath.Join(dir, "snapshot.json")
	data, err := os.ReadFile(path) // #nosec G304 -- snapshot-scoped
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("scheduler snapshot: read: %w", err)
	}
	var env snapshotEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		if logger != nil {
			logger.Warn("scheduler snapshot: corrupt — ignoring", zap.Error(err))
		}
		// Quarantine so the operator can inspect it.
		_ = os.Rename(path, path+".corrupt")
		return nil, nil
	}
	if env.Version > SchemaVersion {
		return nil, fmt.Errorf("scheduler snapshot: newer version %d > %d", env.Version, SchemaVersion)
	}
	return &env, nil
}
