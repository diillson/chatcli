/*
 * ChatCLI - Scheduler: Write-Ahead Log.
 *
 * Identical durability contract as the Reflexion lesson queue:
 *   - Append is synchronous (fsync + atomic rename) before the job is
 *     visible to the in-memory queue.
 *   - Record format is a framed envelope with leading+trailing CRC32
 *     so torn writes are detected and discarded on replay.
 *   - One file per record named by JobID, so ACK is a single unlink.
 *
 * The scheduler WAL differs from lessonq in two ways:
 *
 *   1. Records mutate — a job moves Pending → Waiting → Running →
 *      Completed. Each transition rewrites the .wal file via the same
 *      write-then-rename path, so readers never observe torn state.
 *
 *   2. Terminal jobs are NOT immediately unlinked; they're kept for
 *      TTL so /jobs history can list them, then GC'd. The snapshot
 *      (snapshot.go) periodically freezes the entire state into a
 *      single file so replay is fast even with thousands of records.
 *
 * File layout:
 *
 *   <dir>/
 *     <jobid>.wal     — current record per job
 *     <jobid>.tmp.*   — in-flight Append; leftover tmp on crash = ignore
 *     snapshot.json   — periodic full-state snapshot (see snapshot.go)
 *     snapshot.tmp    — in-flight snapshot write
 */
package scheduler

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// walMagic is the file magic identifying a scheduler WAL record.
var walMagic = [4]byte{'S', 'C', 'H', '1'}

// WALClosed is returned from Append after Close.
var errWALClosedLocal = errors.New("scheduler wal: closed")

// walRecordCap is the per-record size bound. 16 MiB is more than
// enough for any realistic job record (the History ring is capped
// and large tool outputs are not stored inline).
const walRecordCap = 16 << 20

// schedulerWAL is the append-only log for jobs. Safe for concurrent use.
type schedulerWAL struct {
	dir    string
	logger *zap.Logger
	seq    atomic.Uint64
	mu     sync.Mutex
	closed atomic.Bool
}

// walEnvelope wraps a Job with a schema version so future migrations
// don't break on existing records.
type walEnvelope struct {
	Version int  `json:"v"`
	Job     *Job `json:"job"`
}

// newSchedulerWAL opens (creates if necessary) a WAL in dir.
func newSchedulerWAL(dir string, logger *zap.Logger) (*schedulerWAL, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("scheduler wal: mkdir %s: %w", dir, err)
	}
	return &schedulerWAL{dir: dir, logger: logger}, nil
}

// Dir returns the directory backing this WAL.
func (w *schedulerWAL) Dir() string { return w.dir }

// Close marks the WAL closed.
func (w *schedulerWAL) Close() { w.closed.Store(true) }

// Write persists a job record atomically. Used by both initial Append
// and subsequent Update — from a durability standpoint both need to
// commit a full record, so we don't distinguish them.
//
// CONTRACT: the caller MUST hold Job.mu (or have exclusive access to
// the *Job during Enqueue / Replay). WAL.Write never re-enters Job.mu
// so it is safe to call from within a j.lock()/unlock() critical
// section; acquiring j.mu here would deadlock the dispatcher.
func (w *schedulerWAL) Write(j *Job) error {
	if w.closed.Load() {
		return errWALClosedLocal
	}
	if j == nil || j.ID.IsZero() {
		return errors.New("scheduler wal: nil job or empty id")
	}

	payload, err := json.Marshal(walEnvelope{Version: SchemaVersion, Job: j})
	if err != nil {
		return fmt.Errorf("scheduler wal: marshal: %w", err)
	}
	if len(payload) > walRecordCap {
		return fmt.Errorf("scheduler wal: payload too large (%d bytes)", len(payload))
	}
	framed := frameWALRecord(payload)

	w.mu.Lock()
	defer w.mu.Unlock()

	finalPath := filepath.Join(w.dir, recordName(j.ID))
	tmpName := fmt.Sprintf("%s.tmp.%d.%d", string(j.ID), os.Getpid(), w.seq.Add(1))
	tmpPath := filepath.Join(w.dir, tmpName)

	if err := writeAndSyncWAL(tmpPath, framed); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("scheduler wal: rename: %w", err)
	}
	if err := syncWALDir(w.dir); err != nil {
		w.logger.Warn("scheduler wal: dir fsync failed", zap.Error(err))
	}
	return nil
}

// Ack removes the record for id. Idempotent (missing → nil). Used after
// a terminal job's TTL expires.
func (w *schedulerWAL) Ack(id JobID) error {
	if id.IsZero() {
		return nil
	}
	path := filepath.Join(w.dir, recordName(id))
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("scheduler wal: ack %s: %w", id, err)
	}
	return nil
}

// Read returns a single record from the WAL without affecting the
// on-disk state. Used by the daemon to fetch job details on demand.
func (w *schedulerWAL) Read(id JobID) (*Job, error) {
	path := filepath.Join(w.dir, recordName(id))
	return readWALRecord(path)
}

// List scans the directory and returns every valid record. Corrupt
// records are logged and quarantined (renamed to .corrupt) so
// operators can inspect them out-of-band.
//
// Returned jobs are sorted by CreatedAt ascending so replay honors the
// submission order when rescheduling.
func (w *schedulerWAL) List() ([]*Job, error) {
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("scheduler wal: readdir: %w", err)
	}
	out := make([]*Job, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.Contains(name, ".tmp.") {
			// Stale in-flight Append; clean up.
			if rmErr := os.Remove(filepath.Join(w.dir, name)); rmErr != nil {
				w.logger.Warn("scheduler wal: stale tmp cleanup failed",
					zap.String("file", name), zap.Error(rmErr))
			}
			continue
		}
		if !strings.HasSuffix(name, ".wal") {
			continue
		}
		path := filepath.Join(w.dir, name)
		job, rerr := readWALRecord(path)
		if rerr != nil {
			w.logger.Warn("scheduler wal: corrupt record; quarantining",
				zap.String("file", name), zap.Error(rerr))
			corrupt := path + ".corrupt"
			if renErr := os.Rename(path, corrupt); renErr != nil {
				// Best-effort delete if rename fails.
				_ = os.Remove(path)
			}
			continue
		}
		out = append(out, job)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

// Count returns how many live (.wal) records exist without decoding.
func (w *schedulerWAL) Count() int {
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.Contains(name, ".tmp.") {
			continue
		}
		if strings.HasSuffix(name, ".wal") {
			n++
		}
	}
	return n
}

// ─── framing ──────────────────────────────────────────────────────────

func recordName(id JobID) string { return string(id) + ".wal" }

// frameWALRecord emits magic | len | crc | payload | crc.
func frameWALRecord(payload []byte) []byte {
	if uint64(len(payload)) > uint64(math.MaxUint32) {
		return nil
	}
	crc := crc32.ChecksumIEEE(payload)
	var buf bytes.Buffer
	buf.Grow(16 + len(payload))
	buf.Write(walMagic[:])
	// #nosec G115 -- len bounded above by math.MaxUint32 guard
	_ = binary.Write(&buf, binary.BigEndian, uint32(len(payload)))
	_ = binary.Write(&buf, binary.BigEndian, crc)
	buf.Write(payload)
	_ = binary.Write(&buf, binary.BigEndian, crc)
	return buf.Bytes()
}

// readWALRecord parses a single .wal file.
func readWALRecord(path string) (*Job, error) {
	f, err := os.Open(path) // #nosec G304 -- WAL-scoped path, not user-controlled
	if err != nil {
		return nil, err
	}
	defer f.Close()

	br := bufio.NewReader(f)
	head := make([]byte, 12)
	if _, err := br.Read(head); err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	if !bytes.Equal(head[0:4], walMagic[:]) {
		return nil, fmt.Errorf("bad magic")
	}
	length := binary.BigEndian.Uint32(head[4:8])
	crc := binary.BigEndian.Uint32(head[8:12])
	if length == 0 || length > walRecordCap {
		return nil, fmt.Errorf("bad length %d", length)
	}
	payload := make([]byte, length)
	if _, err := br.Read(payload); err != nil {
		return nil, fmt.Errorf("read payload: %w", err)
	}
	if crc32.ChecksumIEEE(payload) != crc {
		return nil, fmt.Errorf("leading crc mismatch")
	}
	tail := make([]byte, 4)
	if _, err := br.Read(tail); err != nil {
		return nil, fmt.Errorf("read trailing crc: %w", err)
	}
	if binary.BigEndian.Uint32(tail) != crc {
		return nil, fmt.Errorf("trailing crc mismatch (torn write?)")
	}
	var env walEnvelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return nil, fmt.Errorf("json: %w", err)
	}
	if env.Job == nil {
		return nil, fmt.Errorf("empty job in envelope")
	}
	// Guard against old records without CreatedAt.
	if env.Job.CreatedAt.IsZero() {
		env.Job.CreatedAt = time.Unix(0, 0)
	}
	return env.Job, nil
}

// ─── fs helpers ────────────────────────────────────────────────────────

func writeAndSyncWAL(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) // #nosec G304 -- WAL-scoped
	if err != nil {
		return fmt.Errorf("scheduler wal: create tmp: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("scheduler wal: write tmp: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("scheduler wal: fsync tmp: %w", err)
	}
	return f.Close()
}

func syncWALDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	f, err := os.Open(dir) // #nosec G304 -- WAL-scoped
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
