/*
 * ChatCLI - Lesson Queue: Write-Ahead Log.
 *
 * The WAL is the source of durability for the queue. Every accepted
 * Enqueue synchronously appends a record here before the job is
 * visible to workers; every ACK (success, skip, or DLQ move) removes
 * it. This means a process crash at any point leaves the on-disk
 * state recoverable:
 *
 *   - Record present, job unprocessed → re-enqueue on boot.
 *   - Record present, job finished    → caller forgot to ACK; safe
 *     to re-process because Processor is idempotent per JobID.
 *   - Record absent, job in flight    → processing pre-crash, we
 *     lose nothing: the record was already ACKed, which means the
 *     lesson persisted successfully (or was explicitly DLQ-moved).
 *
 * File layout: one file per record, named by JobID. This trades
 * slightly more filesystem overhead for O(1) ACK (single unlink) and
 * trivial forensics — operators can `ls` the directory and see what's
 * pending. No background compaction needed.
 *
 * Record format (per file):
 *
 *   [4B magic 'LSN1'][4B length-BE][4B CRC32 of payload][N bytes payload][4B trailing CRC32]
 *
 * Double CRC guards against torn writes: if the OS crashes mid-fsync
 * we might see a truncated file; the trailing CRC ensures we only
 * accept complete records.
 *
 * Atomic append:
 *   1. Write to   <dir>/<id>.wal.tmp.<pid>.<monotonic>
 *   2. f.Sync()
 *   3. os.Rename(tmp, <dir>/<id>.wal)  [atomic on POSIX]
 *   4. syncDir(<dir>)  [durability of the rename itself]
 *
 * The trailing CRC is identical to the leading CRC — we repeat it so
 * the only way for a reader to see both is a fully-flushed write.
 */
package lessonq

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

// magic4 is the WAL record header magic.
var magic4 = [4]byte{'L', 'S', 'N', '1'}

// ErrWALClosed is returned from Append after Close has been called.
var ErrWALClosed = errors.New("lessonq: WAL closed")

// WAL is the append-only log for lesson jobs. A single WAL instance
// owns one directory and is safe for concurrent Append/Ack/List.
type WAL struct {
	dir    string
	logger *zap.Logger
	m      *Metrics

	// seq is a monotonically increasing counter appended to tmp
	// filenames so concurrent Appends with the same pid/timestamp
	// don't collide.
	seq atomic.Uint64

	// mu serializes Append (file creation + rename) within a single
	// process. Rename itself is atomic; the lock keeps the tmp
	// filename generation + fsync race-free without extra syscalls.
	mu sync.Mutex

	closed atomic.Bool
}

// recordEnvelope is the JSON body wrapped by the WAL framing. We keep
// it explicit (rather than encoding LessonJob directly) so we can add
// schema versioning later without a breaking read.
type recordEnvelope struct {
	Version int       `json:"v"`
	Job     LessonJob `json:"job"`
}

const recordVersion = 1

// NewWAL opens (and creates if needed) a WAL in dir. metrics may be
// nil for tests; logger nil is upgraded to a no-op.
func NewWAL(dir string, metrics *Metrics, logger *zap.Logger) (*WAL, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	// 0o750: owner rwx, group rx, no world — WAL records are user-
	// scoped session state, no read-access needed from other users.
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("lessonq wal: mkdir %s: %w", dir, err)
	}
	w := &WAL{dir: dir, logger: logger, m: metrics}
	w.refreshSegmentGauge()
	return w, nil
}

// Dir returns the directory backing this WAL. Useful for tests and
// for the DLQ (which shares the same layout with a different dir).
func (w *WAL) Dir() string { return w.dir }

// Close marks the WAL closed. Subsequent Appends fail with
// ErrWALClosed. Existing files are untouched — reopen the WAL with
// NewWAL pointing at the same dir to resume.
func (w *WAL) Close() {
	w.closed.Store(true)
}

// Append writes a job record durably and returns nil iff the record
// is safely on disk. Concurrency-safe. See AppendNew for a variant
// that signals whether the record was already present.
func (w *WAL) Append(job LessonJob) error {
	_, err := w.AppendNew(job)
	return err
}

// AppendNew is like Append but returns (true, nil) when a new record
// was created and (false, nil) when an idempotent no-op was taken
// (record already present). Callers that need to distinguish first-
// write from retry (e.g. the Runner, which only pushes new records
// onto the in-memory queue) use this variant to avoid double-
// processing jobs that are already in flight.
func (w *WAL) AppendNew(job LessonJob) (bool, error) {
	if w.closed.Load() {
		return false, ErrWALClosed
	}
	if strings.TrimSpace(string(job.ID)) == "" {
		return false, errors.New("lessonq wal: empty JobID")
	}

	payload, err := json.Marshal(recordEnvelope{Version: recordVersion, Job: job})
	if err != nil {
		return false, fmt.Errorf("lessonq wal: marshal: %w", err)
	}
	// Guard against absurdly large payloads (shouldn't happen; truncate
	// upstream). 16 MiB is generous for a single lesson request.
	if len(payload) > 16<<20 {
		return false, fmt.Errorf("lessonq wal: payload too large (%d bytes)", len(payload))
	}

	framed := frameRecord(payload)

	w.mu.Lock()
	defer w.mu.Unlock()

	finalPath := filepath.Join(w.dir, string(job.ID)+".wal")
	// Idempotency: if the file already exists, treat as no-op. Caller
	// (queue.Enqueue) already dedupes on JobID but WAL re-Append on
	// replay should be safe.
	if _, statErr := os.Stat(finalPath); statErr == nil {
		return false, nil
	}

	tmpName := fmt.Sprintf("%s.tmp.%d.%d", string(job.ID), os.Getpid(), w.seq.Add(1))
	tmpPath := filepath.Join(w.dir, tmpName)

	if err := writeAndSync(tmpPath, framed); err != nil {
		_ = os.Remove(tmpPath) // best effort cleanup
		return false, err
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return false, fmt.Errorf("lessonq wal: rename: %w", err)
	}
	if err := syncDir(w.dir); err != nil {
		// Rename already succeeded; the file is visible. A failed dir
		// fsync means durability of the rename is uncertain — log it
		// but return nil. The record is readable on reboot anyway.
		w.logger.Warn("lessonq wal: dir fsync failed", zap.Error(err))
	}
	w.refreshSegmentGauge()
	return true, nil
}

// Ack removes the record for id. Missing records return nil (idempotent
// — tests and retries may ACK the same id twice).
func (w *WAL) Ack(id JobID) error {
	if strings.TrimSpace(string(id)) == "" {
		return nil
	}
	path := filepath.Join(w.dir, string(id)+".wal")
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("lessonq wal: ack %s: %w", id, err)
	}
	w.refreshSegmentGauge()
	return nil
}

// Update rewrites the record for job.ID (used when Attempts /
// NextAttemptAt / LastError change after a transient failure). Uses
// the same write-then-rename path so readers never see a torn record.
func (w *WAL) Update(job LessonJob) error {
	if w.closed.Load() {
		return ErrWALClosed
	}
	if strings.TrimSpace(string(job.ID)) == "" {
		return errors.New("lessonq wal: empty JobID")
	}
	payload, err := json.Marshal(recordEnvelope{Version: recordVersion, Job: job})
	if err != nil {
		return fmt.Errorf("lessonq wal: marshal: %w", err)
	}
	framed := frameRecord(payload)

	w.mu.Lock()
	defer w.mu.Unlock()

	finalPath := filepath.Join(w.dir, string(job.ID)+".wal")
	tmpName := fmt.Sprintf("%s.tmp.%d.%d", string(job.ID), os.Getpid(), w.seq.Add(1))
	tmpPath := filepath.Join(w.dir, tmpName)

	if err := writeAndSync(tmpPath, framed); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("lessonq wal: rename (update): %w", err)
	}
	if err := syncDir(w.dir); err != nil {
		w.logger.Warn("lessonq wal: dir fsync on update failed", zap.Error(err))
	}
	return nil
}

// List scans the WAL directory and returns every valid record. Corrupt
// records are logged (and incremented against WALCorruption) but do
// not abort the scan — one bad record must never lock out the rest.
//
// Returned jobs are sorted by EnqueuedAt ascending so drain processes
// oldest first.
func (w *WAL) List() ([]LessonJob, error) {
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("lessonq wal: readdir: %w", err)
	}
	out := make([]LessonJob, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		// Skip tmp files — they represent an in-flight Append that
		// crashed; the atomic rename never happened. Remove them so
		// they don't accumulate forever.
		if strings.Contains(name, ".tmp.") {
			path := filepath.Join(w.dir, name)
			if rmErr := os.Remove(path); rmErr != nil {
				w.logger.Warn("lessonq wal: stale tmp cleanup failed",
					zap.String("file", name), zap.Error(rmErr))
			}
			continue
		}
		if !strings.HasSuffix(name, ".wal") {
			continue
		}
		path := filepath.Join(w.dir, name)
		job, readErr := readRecord(path)
		if readErr != nil {
			w.logger.Warn("lessonq wal: corrupt record; discarding",
				zap.String("file", name), zap.Error(readErr))
			if w.m != nil {
				w.m.WALCorruption.Inc()
			}
			// Delete the corrupt file so it doesn't get re-read every
			// boot. Operators keep a copy via backup if they care.
			if rmErr := os.Remove(path); rmErr != nil {
				w.logger.Warn("lessonq wal: failed to delete corrupt record",
					zap.String("file", name), zap.Error(rmErr))
			}
			continue
		}
		out = append(out, job)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].EnqueuedAt.Before(out[j].EnqueuedAt)
	})
	w.refreshSegmentGauge()
	return out, nil
}

// Count returns how many valid .wal files live in the dir without
// parsing them. Used by metrics to keep a gauge accurate without re-
// decoding every record.
func (w *WAL) Count() int {
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

// refreshSegmentGauge keeps the Prometheus gauge aligned with
// actual disk state. Cheap (single readdir) and called from mutating
// paths — the metric is only approximate during concurrent work,
// which is fine for observability.
func (w *WAL) refreshSegmentGauge() {
	if w.m == nil {
		return
	}
	w.m.WALSegments.Set(float64(w.Count()))
}

// ─── framing helpers ──────────────────────────────────────────────────────

// frameRecord wraps payload in the WAL envelope:
//
//	magic | len | crc32(payload) | payload | crc32(payload)
//
// The caller (AppendNew) bounds payload length to 16 MiB before
// invoking this function — well under the uint32 max of 4 GiB — so
// the length cast is safe by construction. The guard below turns
// that invariant into a runtime check in case a future caller forgets.
func frameRecord(payload []byte) []byte {
	if uint64(len(payload)) > uint64(math.MaxUint32) {
		// Defensive: upstream AppendNew enforces a 16 MiB cap, but if
		// this is ever called from elsewhere, refuse to produce a
		// malformed record (callers of frameRecord get an empty
		// buffer, which readRecord will reject on bad magic).
		return nil
	}
	crc := crc32.ChecksumIEEE(payload)
	var buf bytes.Buffer
	buf.Grow(16 + len(payload))
	buf.Write(magic4[:])
	// #nosec G115 -- len bounded above by math.MaxUint32
	_ = binary.Write(&buf, binary.BigEndian, uint32(len(payload)))
	_ = binary.Write(&buf, binary.BigEndian, crc)
	buf.Write(payload)
	_ = binary.Write(&buf, binary.BigEndian, crc)
	return buf.Bytes()
}

// readRecord parses a .wal file and returns the LessonJob. Errors on
// bad magic, length overflow, CRC mismatch (either leading or
// trailing), or JSON parse failure.
//
// path is always a *.wal file inside our WAL dir (from filepath.Join of
// the WAL base dir and a validated .wal suffix). Not user-supplied.
func readRecord(path string) (LessonJob, error) {
	f, err := os.Open(path) // #nosec G304 -- path is WAL-scoped, not user-controlled

	if err != nil {
		return LessonJob{}, err
	}
	defer f.Close()

	br := bufio.NewReader(f)
	head := make([]byte, 12) // magic(4) + len(4) + crc(4)
	if _, err := br.Read(head); err != nil {
		return LessonJob{}, fmt.Errorf("read header: %w", err)
	}
	if !bytes.Equal(head[0:4], magic4[:]) {
		return LessonJob{}, fmt.Errorf("bad magic")
	}
	length := binary.BigEndian.Uint32(head[4:8])
	crc := binary.BigEndian.Uint32(head[8:12])
	if length == 0 || length > 16<<20 {
		return LessonJob{}, fmt.Errorf("bad length %d", length)
	}

	payload := make([]byte, length)
	if _, err := br.Read(payload); err != nil {
		return LessonJob{}, fmt.Errorf("read payload: %w", err)
	}
	if crc32.ChecksumIEEE(payload) != crc {
		return LessonJob{}, fmt.Errorf("leading crc mismatch")
	}

	tail := make([]byte, 4)
	if _, err := br.Read(tail); err != nil {
		return LessonJob{}, fmt.Errorf("read trailing crc: %w", err)
	}
	if binary.BigEndian.Uint32(tail) != crc {
		return LessonJob{}, fmt.Errorf("trailing crc mismatch (torn write?)")
	}

	var env recordEnvelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return LessonJob{}, fmt.Errorf("json: %w", err)
	}
	if env.Version != recordVersion {
		return LessonJob{}, fmt.Errorf("unsupported record version %d", env.Version)
	}
	// Defensive: if EnqueuedAt is zero (old data), bump to "a long
	// time ago" so drain still processes it but stale-filtering fires.
	if env.Job.EnqueuedAt.IsZero() {
		env.Job.EnqueuedAt = time.Unix(0, 0)
	}
	return env.Job, nil
}

// ─── fs helpers ────────────────────────────────────────────────────────────

// writeAndSync writes data to path and fsyncs the file. Caller must
// rename into place afterwards.
//
// path is always a tmp file inside our WAL dir (constructed from the
// JobID + pid + monotonic counter) — not user-controlled at this layer.
// 0o600 restricts access to the owner: WAL records carry task text and
// last-error messages that may contain tokens or PII.
func writeAndSync(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) // #nosec G304 -- path is WAL-scoped, not user-controlled
	if err != nil {
		return fmt.Errorf("lessonq wal: create tmp: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("lessonq wal: write tmp: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("lessonq wal: fsync tmp: %w", err)
	}
	return f.Close()
}

// syncDir fsyncs a directory so a preceding rename is durable. On
// Windows this is a no-op (dir fsync is not supported and not needed
// — NTFS provides rename atomicity through its journal).
//
// dir is the WAL base dir passed from NewWAL; scoped to our
// subsystem and never user-supplied at call time.
func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	f, err := os.Open(dir) // #nosec G304 -- dir is WAL-scoped, not user-controlled
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
