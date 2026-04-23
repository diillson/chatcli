/*
 * ChatCLI - Scheduler: boot-time replay.
 *
 * Boot order:
 *   1. Try the snapshot — fast path. If present and version-compatible,
 *      hydrate the in-memory map from it, then overlay any .wal records
 *      not covered by the snapshot.
 *   2. If no usable snapshot, fall back to a full WAL scan.
 *   3. For every live (non-terminal) job, re-enqueue it with an
 *      adjusted NextFireAt according to MissPolicy.
 *
 * MissPolicy semantics on replay:
 *   fire_once — coalesce all missed ticks into a single fire at now.
 *   fire_all  — schedule now, and scheduler.maybeReschedule will keep
 *               firing as the queue catches up (bounded by the natural
 *               cron tick so we don't runaway).
 *   skip      — leave NextFireAt in the future; missed windows are
 *               considered a no-op.
 */
package scheduler

import (
	"sort"
	"time"

	"go.uber.org/zap"
)

// replay restores state from disk. Called from Start exactly once.
func (s *Scheduler) replay() error {
	snapJobs := map[JobID]bool{}

	if env, err := readSnapshot(s.cfg.DataDir, s.logger); err == nil && env != nil {
		s.logger.Info("scheduler: loading snapshot",
			zap.Time("captured_at", env.CapturedAt),
			zap.Int("jobs", len(env.Jobs)))
		for _, j := range env.Jobs {
			if j == nil || j.ID.IsZero() {
				continue
			}
			s.jobs[j.ID] = j
			if !j.Status.IsTerminal() {
				s.byName[j.Name] = j.ID
			}
			snapJobs[j.ID] = true
		}
	}

	// Overlay WAL — any record present in WAL overrides the snapshot
	// (WAL is the truth; snapshot is a cache).
	walJobs, err := s.wal.List()
	if err != nil {
		return err
	}
	for _, j := range walJobs {
		if j == nil || j.ID.IsZero() {
			continue
		}
		s.jobs[j.ID] = j
		if !j.Status.IsTerminal() {
			s.byName[j.Name] = j.ID
		}
	}

	// Reschedule live jobs.
	now := time.Now()
	// Sort by NextFireAt so the queue order mirrors what it would have
	// been pre-crash. Stable for ties.
	live := make([]*Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		if !j.Status.IsTerminal() && j.Status != StatusPaused && j.Status != StatusBlocked {
			live = append(live, j)
		}
	}
	sort.SliceStable(live, func(i, j int) bool {
		return live[i].NextFireAt.Before(live[j].NextFireAt)
	})

	for _, j := range live {
		next := j.NextFireAt
		if next.IsZero() || next.Before(now) {
			switch j.Schedule.MissPolicy {
			case MissSkip:
				// Forward to next natural fire.
				n := j.Schedule.Next(now, j.CreatedAt)
				if n.IsZero() {
					s.logger.Info("scheduler: replay skipping terminal-schedule job",
						zap.String("job_id", string(j.ID)))
					continue
				}
				next = n
			default:
				next = now
			}
			j.NextFireAt = next
			_ = s.wal.Write(j)
		}
		// Jobs that were Running at the time of the crash must restart
		// cleanly — transition them back to Pending and bump Attempts
		// so retry budget stays honest.
		if j.Status == StatusRunning || j.Status == StatusWaiting {
			prev := j.Status
			_ = j.transition(StatusPending, "replay: interrupted mid-run", s.logger)
			_ = s.wal.Write(j)
			s.logger.Info("scheduler: replay resuming interrupted job",
				zap.String("job_id", string(j.ID)),
				zap.String("prev_status", string(prev)))
		}
		s.queue.Enqueue(j.ID, j.NextFireAt)
	}

	// Re-check blocked jobs — any dep that is now terminal in a
	// non-success state should cascade into a failed child.
	for _, j := range s.jobs {
		if j.Status != StatusBlocked {
			continue
		}
		ready := true
		failedDep := JobID("")
		for _, dep := range j.DependsOn {
			d, ok := s.jobs[dep]
			if !ok {
				continue
			}
			if !d.Status.IsTerminal() {
				ready = false
				break
			}
			if d.Status != StatusCompleted {
				failedDep = dep
				ready = false
				break
			}
		}
		if failedDep != "" {
			_ = j.transition(StatusFailed, "replay: dependency "+string(failedDep)+" ended non-successfully", s.logger)
			_ = s.wal.Write(j)
			continue
		}
		if ready {
			_ = j.transition(StatusPending, "replay: deps resolved", s.logger)
			if j.NextFireAt.IsZero() {
				j.NextFireAt = now
			}
			_ = s.wal.Write(j)
			s.queue.Enqueue(j.ID, j.NextFireAt)
		}
	}

	// Metric snapshot.
	s.metrics.QueueDepth.Set(float64(s.queue.Len()))
	s.metrics.ActiveJobs.Set(float64(s.activeCount()))
	return nil
}

// writeSnapshotNow captures state. Exposed for Shutdown and test helpers.
func (s *Scheduler) writeSnapshotNow() error {
	s.mu.RLock()
	jobs := make([]*Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		jobs = append(jobs, j.cloneLocked())
	}
	s.mu.RUnlock()
	return writeSnapshot(s.cfg.DataDir, jobs, s.logger)
}

// snapshotLoop runs periodic writeSnapshot while the scheduler is up.
func (s *Scheduler) snapshotLoop() {
	defer close(s.snapDone)
	if s.cfg.SnapshotInterval <= 0 {
		return
	}
	t := time.NewTicker(s.cfg.SnapshotInterval)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
			if err := s.writeSnapshotNow(); err != nil {
				s.logger.Warn("scheduler: snapshot failed", zap.Error(err))
			}
		}
	}
}

// gcLoop reaps terminal jobs past TTL.
func (s *Scheduler) gcLoop() {
	defer close(s.gcDone)
	if s.cfg.WALGCInterval <= 0 {
		return
	}
	t := time.NewTicker(s.cfg.WALGCInterval)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
			s.gcOnce()
		}
	}
}

func (s *Scheduler) gcOnce() {
	now := time.Now()
	var toAck []JobID
	s.mu.RLock()
	for id, j := range s.jobs {
		if j.IsExpired(now) {
			toAck = append(toAck, id)
		}
	}
	s.mu.RUnlock()
	for _, id := range toAck {
		if err := s.wal.Ack(id); err != nil {
			s.logger.Warn("scheduler: gc wal ack failed", zap.String("id", string(id)), zap.Error(err))
			continue
		}
		s.mu.Lock()
		delete(s.jobs, id)
		s.mu.Unlock()
	}
	if len(toAck) > 0 {
		s.logger.Info("scheduler: gc reaped", zap.Int("count", len(toAck)))
		s.metrics.WALSegments.Set(float64(s.wal.Count()))
		s.metrics.ActiveJobs.Set(float64(s.activeCount()))
		s.version.Add(1)
	}
}
