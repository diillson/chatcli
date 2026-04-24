/*
 * ChatCLI - Scheduler: Job state machine.
 *
 * The state machine serves three purposes:
 *
 *   1. Enforce legal transitions — rejecting bugs that try to push a
 *      job from (for example) Completed back to Running.
 *
 *   2. Centralize the side-effects of a transition — metrics, audit
 *      log entry, event-bus publish, and hook fire. Every transition
 *      path produces the same observability signal with no duplication.
 *
 *   3. Make concurrent mutation safe. Each Job owns a sync.Mutex that
 *      Scheduler methods lock for the duration of the transition so no
 *      two goroutines race over the state.
 *
 * Legal transitions (read top→bottom; missing edges are illegal):
 *
 *   Pending   → Blocked, Waiting, Running, Paused, Cancelled, Skipped
 *   Blocked   → Pending, Cancelled
 *   Waiting   → Running, TimedOut, Cancelled, Failed
 *   Running   → Completed, Failed, TimedOut, Cancelled
 *   Paused    → Pending, Cancelled
 *   (terminal) Completed / Failed / Cancelled / TimedOut / Skipped
 *
 * The scheduler never resurrects terminal jobs. A recurring cron job
 * doesn't "go back to pending" on the same Job record — it spawns a
 * fresh occurrence (sibling Job) with a new ID.
 */
package scheduler

import (
	"fmt"
	"time"

	"go.uber.org/zap"
)

// transitionTable encodes which source states may transition to which
// target states. The empty set for a status means "terminal".
var transitionTable = map[JobStatus]map[JobStatus]struct{}{
	StatusPending: {
		StatusBlocked:   {},
		StatusWaiting:   {},
		StatusRunning:   {},
		StatusPaused:    {},
		StatusCancelled: {},
		StatusSkipped:   {},
	},
	StatusBlocked: {
		StatusPending:   {},
		StatusCancelled: {},
		StatusPaused:    {},
	},
	StatusWaiting: {
		StatusRunning:   {},
		StatusTimedOut:  {},
		StatusCancelled: {},
		StatusFailed:    {},
	},
	StatusRunning: {
		StatusCompleted: {},
		StatusFailed:    {},
		StatusTimedOut:  {},
		StatusCancelled: {},
	},
	StatusPaused: {
		StatusPending:   {},
		StatusCancelled: {},
	},
	StatusCompleted: {},
	StatusFailed:    {},
	StatusCancelled: {},
	StatusTimedOut:  {},
	StatusSkipped:   {},
}

// canTransition reports whether src → dst is a legal transition.
func canTransition(src, dst JobStatus) bool {
	edges, ok := transitionTable[src]
	if !ok {
		return false
	}
	_, allowed := edges[dst]
	return allowed
}

// TransitionReason carries human-readable context for audit/event.
// Separated from the status because the same status may be reached via
// different paths (e.g. StatusFailed after action error vs after
// circuit breaker rejection).
type TransitionReason struct {
	From    JobStatus
	To      JobStatus
	Message string
	At      time.Time
}

// transition atomically moves the job from expected to target iff the
// current state is expected. Returns the applied reason record.
//
// Callers hold the job's mutex for the duration of this call.
//
// Invariants enforced here:
//   - Target must be reachable from From in transitionTable.
//   - UpdatedAt is advanced to the current time.
//   - A terminal state records FinishedAt in the job.
func (j *Job) transition(target JobStatus, message string, logger *zap.Logger) error {
	if !canTransition(j.Status, target) {
		return fmt.Errorf("scheduler: illegal transition %s→%s: %w",
			j.Status, target, ErrJobTerminal)
	}
	reason := TransitionReason{
		From:    j.Status,
		To:      target,
		Message: message,
		At:      time.Now(),
	}
	if logger != nil {
		logger.Debug("scheduler: job transition",
			zap.String("job_id", string(j.ID)),
			zap.String("name", j.Name),
			zap.String("from", string(reason.From)),
			zap.String("to", string(reason.To)),
			zap.String("message", message),
		)
	}
	j.Status = target
	j.UpdatedAt = reason.At
	j.Transitions = append(j.Transitions, reason)

	// Cap transitions history so very long-running recurring jobs
	// don't bloat the WAL record. 64 is enough to see a whole lifecycle
	// plus some retries.
	if len(j.Transitions) > 64 {
		j.Transitions = j.Transitions[len(j.Transitions)-64:]
	}

	if target.IsTerminal() && j.FinishedAt.IsZero() {
		j.FinishedAt = reason.At
	}
	return nil
}

// recordExecution appends an ExecutionResult to the job's history ring
// buffer, respecting HistoryLimit. Caller holds the job mutex.
func (j *Job) recordExecution(r ExecutionResult) {
	limit := j.HistoryLimit
	if limit <= 0 {
		limit = 16
	}
	j.History = append(j.History, r)
	if len(j.History) > limit {
		j.History = j.History[len(j.History)-limit:]
	}
	last := r
	j.LastResult = &last
}
