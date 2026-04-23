/*
 * ChatCLI - Lesson Queue: Dead Letter Queue operations.
 *
 * The DLQ is its own WAL directory. Entries live there until an
 * operator explicitly purges them or replays them back to the
 * active queue via the /reflect slash commands.
 *
 * Layout: the DLQ shares the WAL file format with the active queue
 * so the same reader code works for both. The only difference is
 * that workers never pull from the DLQ — it's read-only to the
 * process, mutated only by DLQ operations in this file.
 */
package lessonq

import (
	"fmt"
	"path/filepath"

	"go.uber.org/zap"
)

// DLQ wraps a WAL-backed dead letter queue. Not safe for concurrent
// mutation from outside (ops like Replay+Purge are admin-only and
// rare); internal WAL locking handles the file-side concurrency.
type DLQ struct {
	wal    *WAL
	logger *zap.Logger
	m      *Metrics
}

// NewDLQ opens a DLQ rooted at dir. dir is typically <base>/dlq next
// to the active WAL. A nil logger is upgraded to a no-op so the
// caller never has to nil-check.
func NewDLQ(dir string, metrics *Metrics, logger *zap.Logger) (*DLQ, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	w, err := NewWAL(dir, metrics, logger)
	if err != nil {
		return nil, fmt.Errorf("lessonq dlq: open wal: %w", err)
	}
	return &DLQ{wal: w, logger: logger, m: metrics}, nil
}

// Dir returns the directory backing this DLQ.
func (d *DLQ) Dir() string { return d.wal.Dir() }

// Put writes a job to the DLQ. Used by the Runner when MaxAttempts
// is reached on a transient error chain, or immediately on a
// permanent error.
func (d *DLQ) Put(job LessonJob) error {
	if err := d.wal.Append(job); err != nil {
		return err
	}
	d.refreshGauge()
	return nil
}

// List returns all DLQ jobs in EnqueuedAt order.
func (d *DLQ) List() ([]LessonJob, error) {
	jobs, err := d.wal.List()
	if err != nil {
		return nil, err
	}
	d.refreshGauge()
	return jobs, nil
}

// Remove deletes a DLQ entry by ID. Used by /reflect purge.
func (d *DLQ) Remove(id JobID) error {
	if err := d.wal.Ack(id); err != nil {
		return err
	}
	d.refreshGauge()
	return nil
}

// Pop returns and removes a DLQ entry by ID. Used by /reflect retry,
// which hands the job back to the active queue. Returns (job, false)
// if the ID is unknown.
func (d *DLQ) Pop(id JobID) (LessonJob, bool, error) {
	path := filepath.Join(d.wal.Dir(), string(id)+".wal")
	job, err := readRecord(path)
	if err != nil {
		// Missing / corrupt — treat as not found, let caller decide.
		d.logger.Debug("lessonq dlq: pop: record not readable",
			zap.String("id", string(id)), zap.Error(err))
		return LessonJob{}, false, nil
	}
	if err := d.wal.Ack(id); err != nil {
		return LessonJob{}, false, err
	}
	d.refreshGauge()
	return job, true, nil
}

// Count returns the current DLQ size.
func (d *DLQ) Count() int { return d.wal.Count() }

// Close signals shutdown. Subsequent Put calls fail.
func (d *DLQ) Close() { d.wal.Close() }

func (d *DLQ) refreshGauge() {
	if d.m == nil {
		return
	}
	d.m.DLQSize.Set(float64(d.wal.Count()))
}
