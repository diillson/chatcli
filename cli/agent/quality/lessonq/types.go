/*
 * ChatCLI - Lesson Queue: core types.
 *
 * Package lessonq provides a durable, crash-consistent queue for
 * Reflexion lesson generation. It sits between the ReflexionHook
 * (which emits LessonRequests as quality triggers fire) and the
 * LLM+persistence layer (which materializes and stores Lessons).
 *
 * Durability model:
 *   1. Enqueue writes a WAL record with CRC32 + fsync + atomic rename.
 *   2. Worker dequeues and processes (LLM call → Lesson → persist).
 *   3. On success, WAL ACKs the entry (deletion or segment rotation).
 *   4. On retryable failure, reschedule with exponential backoff + jitter.
 *   5. On permanent failure or MaxAttempts, move to DLQ (another WAL).
 *   6. Drain-on-boot scans WAL and re-enqueues pending entries.
 *
 * All types here are value types so the package stays easy to mock
 * and feed in tests. Behavior lives in queue.go, worker.go, wal.go.
 */
package lessonq

import (
	"context"
	"time"

	"github.com/diillson/chatcli/cli/agent/quality"
	"github.com/diillson/chatcli/models"
)

// JobID uniquely identifies a lesson job across the entire lifecycle
// (queue, in-flight, DLQ, replay). It is the idempotency key — re-
// enqueueing the same JobID is a no-op.
//
// JobID is derived from sha256(task | trigger | attempt_hash), truncated
// to 16 hex chars for log readability. See dedupe.go for the builder.
type JobID string

// LessonJob is the unit of work the queue processes.
//
// Invariants:
//   - ID is stable across retries (dedupe key).
//   - Request is immutable after Enqueue — retries reuse it verbatim.
//   - Attempts tracks how many processing tries have happened, including
//     the current one. 0 before first dequeue, N after Nth failure.
//   - NextAttemptAt is the earliest time the worker should pick it up.
//     Workers skip jobs whose NextAttemptAt is in the future.
type LessonJob struct {
	ID            JobID
	Request       quality.LessonRequest
	EnqueuedAt    time.Time
	NextAttemptAt time.Time // scheduling hint; workers honor back-off
	Attempts      int
	LastError     string // human-readable last failure, for DLQ inspection
}

// Age returns how long ago the job was enqueued. Used for stale-discard.
func (j LessonJob) Age(now time.Time) time.Duration {
	return now.Sub(j.EnqueuedAt)
}

// ProcessOutcome classifies the result of a single processing attempt.
// Workers map this to either an ACK (success), a reschedule, or a DLQ
// move. Mapping lives in Runner.handleOutcome.
type ProcessOutcome int

const (
	// OutcomeSuccess — lesson generated + persisted. ACK and drop.
	OutcomeSuccess ProcessOutcome = iota
	// OutcomeSkipped — LLM declared "no actionable lesson". ACK and
	// drop (this is a valid terminal state, not an error).
	OutcomeSkipped
	// OutcomeTransient — processing failed with a retryable error
	// (LLM 429/503, fs temp error). Reschedule with back-off.
	OutcomeTransient
	// OutcomePermanent — processing failed with an unrecoverable error
	// (parser failure, config error). Move to DLQ immediately.
	OutcomePermanent
)

// String makes outcomes log-friendly.
func (o ProcessOutcome) String() string {
	switch o {
	case OutcomeSuccess:
		return "success"
	case OutcomeSkipped:
		return "skipped"
	case OutcomeTransient:
		return "transient"
	case OutcomePermanent:
		return "permanent"
	default:
		return "unknown"
	}
}

// ProcessResult carries the outcome of a single processing attempt
// back from the worker function to the Runner. Err is populated for
// OutcomeTransient and OutcomePermanent; ignored otherwise.
type ProcessResult struct {
	Outcome ProcessOutcome
	Err     error
}

// OverflowPolicy governs what happens when the bounded queue is full
// at Enqueue time. Block is the enterprise default (WAL already has
// the record, caller blocks briefly waiting for a slot); DropOldest
// is available for throughput-sensitive deployments.
type OverflowPolicy int

const (
	// OverflowBlock waits up to EnqueueTimeout for a slot, then errors.
	OverflowBlock OverflowPolicy = iota
	// OverflowDropOldest evicts the oldest in-memory job (WAL-backed so
	// it'll be picked up on the next drain) to make room. Used for
	// latency-sensitive callers that never want to block.
	OverflowDropOldest
)

// RetryPolicy governs back-off between transient-failure attempts.
// All fields have safe defaults in DefaultRetryPolicy().
type RetryPolicy struct {
	InitialDelay   time.Duration // first retry waits this long
	MaxDelay       time.Duration // cap on the exponential ramp
	Multiplier     float64       // typically 2.0
	JitterFraction float64       // 0.0–0.5; fraction of delay added as uniform jitter
	MaxAttempts    int           // total attempts (1 = no retries)
}

// DefaultRetryPolicy returns production-ready defaults: 5 attempts,
// 1s → 5min ramp, 20% jitter.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		InitialDelay:   time.Second,
		MaxDelay:       5 * time.Minute,
		Multiplier:     2.0,
		JitterFraction: 0.2,
		MaxAttempts:    5,
	}
}

// Processor is the callback the Runner invokes per-job. Implementations
// perform the LLM call, parse the response, persist the lesson, and
// return a ProcessResult classifying what happened.
//
// The ctx passed here is bounded by a per-job budget (configurable),
// distinct from the caller's ctx that fired the trigger — reflexion
// outlives the turn by design.
type Processor func(ctx context.Context, job LessonJob) ProcessResult

// LLMCaller mirrors quality.LessonLLM but is re-declared here so the
// package stays importable without pulling the full quality surface in
// test paths. Runner.NewProcessor adapts a quality.LessonLLM into this
// shape; see queue_runner.go.
type LLMCaller func(ctx context.Context, history []models.Message) (string, error)

// PersistFn writes a materialized lesson into long-term memory. Mirrors
// quality.PersistLessonFunc.
type PersistFn func(ctx context.Context, lesson quality.Lesson) error
