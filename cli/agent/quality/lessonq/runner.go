/*
 * ChatCLI - Lesson Queue: Runner (public entry point).
 *
 * The Runner composes WAL + Queue + DLQ + worker pool + retry policy
 * into the single object the rest of the codebase interacts with.
 * ReflexionHook.Enqueue → Runner.Enqueue; /reflect drain → Runner.Drain;
 * process startup → Runner.Start + Runner.Replay(WAL).
 *
 * Ownership:
 *   - Runner is the only component that mutates both the WAL and the
 *     in-memory queue under the durability contract (WAL written
 *     BEFORE queue visible).
 *   - Workers read via Queue.Dequeue and call the injected Processor.
 *   - Runner classifies the ProcessResult and updates WAL/queue/DLQ.
 *
 * Lifecycle:
 *
 *   rnr, _ := lessonq.NewRunner(cfg, metrics, logger)
 *   rnr.Start(ctx, processor)             // spins up workers
 *   pending := rnr.Replay(ctx)            // drain WAL → queue
 *   rnr.Enqueue(ctx, request)             // from the hook
 *   // ...
 *   rnr.Shutdown(30 * time.Second)        // on process exit
 */
package lessonq

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"path/filepath"
	"sync"
	"time"

	"github.com/diillson/chatcli/cli/agent/quality"
	"go.uber.org/zap"
)

// RunnerConfig bundles all the knobs the Runner needs. Defaults
// (DefaultRunnerConfig) give a production-safe baseline.
type RunnerConfig struct {
	// BaseDir is where the WAL and DLQ subdirs live. Typically
	// <workspaceDir>/.chatcli/reflexion.
	BaseDir string

	// Workers is the number of goroutines concurrently processing
	// jobs. Default 2 — lesson gen is I/O-bound on the LLM call, so
	// parallelism beyond 2-3 buys little.
	Workers int

	// QueueCapacity is the max in-memory queue depth. Enqueue honors
	// OverflowPolicy when full. Default 1000.
	QueueCapacity int

	// OverflowPolicy: Block | DropOldest. Default Block.
	OverflowPolicy OverflowPolicy

	// EnqueueBlockTimeout is how long Enqueue will wait on a full
	// queue before returning ErrQueueFull. Default 5s.
	EnqueueBlockTimeout time.Duration

	// Retry controls per-job back-off + max attempts.
	Retry RetryPolicy

	// PerJobTimeout bounds a single Processor invocation. Default
	// 2 minutes — lesson generation is a short LLM call but we
	// accept slow providers.
	PerJobTimeout time.Duration

	// StaleAfter discards Replay entries older than this at drain
	// time. Default 7 days — lessons about stale task contexts are
	// usually not useful. Set to 0 to disable.
	StaleAfter time.Duration
}

// DefaultRunnerConfig returns production defaults. BaseDir is empty
// and must be set by the caller.
func DefaultRunnerConfig() RunnerConfig {
	return RunnerConfig{
		Workers:             2,
		QueueCapacity:       1000,
		OverflowPolicy:      OverflowBlock,
		EnqueueBlockTimeout: 5 * time.Second,
		Retry:               DefaultRetryPolicy(),
		PerJobTimeout:       2 * time.Minute,
		StaleAfter:          7 * 24 * time.Hour,
	}
}

// Runner is the composed durable-queue engine.
type Runner struct {
	cfg     RunnerConfig
	wal     *WAL
	dlq     *DLQ
	queue   *Queue
	metrics *Metrics
	logger  *zap.Logger

	// processor is set by Start. Nil before Start → Enqueue still
	// works (records land in WAL + queue), they just won't be
	// processed until Start is called. Useful for boot sequencing.
	processorMu sync.RWMutex
	processor   Processor

	// workerWG tracks in-flight worker goroutines for Shutdown.
	workerWG sync.WaitGroup

	// cancelWorkers terminates all worker goroutines.
	cancelWorkers context.CancelFunc

	started bool
	startMu sync.Mutex

	// rng is seeded once and shared across goroutines via a mutex.
	// Splitting per-worker would marginally reduce contention but
	// this is only touched on retry scheduling — hot path is the
	// LLM call.
	rngMu sync.Mutex
	rng   *rand.Rand
}

// NewRunner builds a Runner and opens its WAL + DLQ. Does NOT start
// workers — call Start + Replay separately.
func NewRunner(cfg RunnerConfig, logger *zap.Logger) (*Runner, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	if cfg.BaseDir == "" {
		return nil, errors.New("lessonq runner: BaseDir required")
	}
	// Normalize defaults so a zero config works.
	if cfg.Workers <= 0 {
		cfg.Workers = 2
	}
	if cfg.QueueCapacity <= 0 {
		cfg.QueueCapacity = 1000
	}
	if cfg.EnqueueBlockTimeout <= 0 {
		cfg.EnqueueBlockTimeout = 5 * time.Second
	}
	if cfg.PerJobTimeout <= 0 {
		cfg.PerJobTimeout = 2 * time.Minute
	}
	if cfg.StaleAfter < 0 {
		cfg.StaleAfter = 0
	}

	metrics := GetMetrics()

	wal, err := NewWAL(filepath.Join(cfg.BaseDir, "wal"), metrics, logger)
	if err != nil {
		return nil, fmt.Errorf("lessonq runner: open wal: %w", err)
	}
	dlq, err := NewDLQ(filepath.Join(cfg.BaseDir, "dlq"), metrics, logger)
	if err != nil {
		return nil, fmt.Errorf("lessonq runner: open dlq: %w", err)
	}

	q := NewQueue(cfg.QueueCapacity, cfg.OverflowPolicy, cfg.EnqueueBlockTimeout, metrics)

	return &Runner{
		cfg:     cfg,
		wal:     wal,
		dlq:     dlq,
		queue:   q,
		metrics: metrics,
		logger:  logger,
		rng:     rand.New(rand.NewSource(time.Now().UnixNano())),
	}, nil
}

// Start spawns the worker pool. Processor must be non-nil.
// Start is idempotent — calling twice returns the first error if any.
func (r *Runner) Start(ctx context.Context, processor Processor) error {
	r.startMu.Lock()
	defer r.startMu.Unlock()

	if r.started {
		return nil
	}
	if processor == nil {
		return errors.New("lessonq runner: nil processor")
	}
	r.processorMu.Lock()
	r.processor = processor
	r.processorMu.Unlock()

	workerCtx, cancel := context.WithCancel(ctx)
	r.cancelWorkers = cancel

	for i := 0; i < r.cfg.Workers; i++ {
		r.workerWG.Add(1)
		go r.workerLoop(workerCtx, i)
	}
	r.started = true
	r.logger.Info("lessonq runner: started",
		zap.Int("workers", r.cfg.Workers),
		zap.String("wal_dir", r.wal.Dir()),
		zap.String("dlq_dir", r.dlq.Dir()),
	)
	return nil
}

// Enqueue accepts a LessonRequest: derives its JobID, writes the WAL
// record durably, and hands the job to the in-memory queue. Returns
// nil on accept (including the dedup case — idempotent).
func (r *Runner) Enqueue(ctx context.Context, req quality.LessonRequest) error {
	id := DeriveJobID(req)
	now := time.Now()
	job := LessonJob{
		ID:            id,
		Request:       req,
		EnqueuedAt:    now,
		NextAttemptAt: now,
		Attempts:      0,
	}
	// Durability FIRST: WAL before queue. If WAL fails we don't
	// enqueue at all — the caller sees an error and can log it.
	newRecord, err := r.wal.AppendNew(job)
	if err != nil {
		return fmt.Errorf("lessonq runner: wal append: %w", err)
	}
	if !newRecord {
		// WAL already has this ID — means the job is either already
		// in the in-memory queue OR currently in flight on a worker
		// (dequeued but not yet ACKed). Either way, don't re-enqueue:
		// idempotent success. This cross-phase dedupe is the reason
		// the hook can fire at high rates without inflating work.
		return nil
	}
	// Try to push into the queue. If queue rejects (full + blocking
	// timed out), the WAL record lingers and will be drained on the
	// next boot — so the lesson isn't lost, just delayed.
	if err := r.queue.Enqueue(ctx, job); err != nil {
		if errors.Is(err, ErrDuplicate) {
			return nil // idempotent success
		}
		if errors.Is(err, ErrQueueFull) {
			// Leave the WAL record; drain-on-boot will pick it up.
			r.logger.Warn("lessonq runner: queue saturated, record kept in WAL",
				zap.String("job_id", string(id)))
			return err
		}
		return err
	}
	return nil
}

// Replay reads the WAL and re-enqueues every pending job. Called once
// on boot (before or after Start — both are safe). Jobs older than
// StaleAfter are discarded with StaleDiscarded metric bumped.
//
// Returns the number of jobs enqueued (not counting stale discards).
func (r *Runner) Replay(ctx context.Context) (int, error) {
	jobs, err := r.wal.List()
	if err != nil {
		return 0, fmt.Errorf("lessonq runner: replay list: %w", err)
	}
	now := time.Now()
	loaded := 0
	for _, job := range jobs {
		// Stale cutoff.
		if r.cfg.StaleAfter > 0 && job.Age(now) > r.cfg.StaleAfter {
			r.logger.Info("lessonq runner: discarding stale record",
				zap.String("job_id", string(job.ID)),
				zap.Duration("age", job.Age(now)))
			if r.metrics != nil {
				r.metrics.StaleDiscarded.Inc()
			}
			if err := r.wal.Ack(job.ID); err != nil {
				r.logger.Warn("lessonq runner: failed to ack stale record",
					zap.String("job_id", string(job.ID)), zap.Error(err))
			}
			continue
		}
		// Reset NextAttemptAt so jobs scheduled for the future pre-
		// crash don't get stuck indefinitely — they become eligible
		// immediately on boot. Preserves Attempts count.
		if job.NextAttemptAt.After(now) {
			job.NextAttemptAt = now
		}
		if err := r.queue.Enqueue(ctx, job); err != nil {
			if errors.Is(err, ErrDuplicate) {
				continue // already in queue (e.g. concurrent enqueue), skip
			}
			r.logger.Warn("lessonq runner: replay enqueue failed",
				zap.String("job_id", string(job.ID)), zap.Error(err))
			continue
		}
		loaded++
	}
	r.logger.Info("lessonq runner: replay complete",
		zap.Int("loaded", loaded),
		zap.Int("total_records", len(jobs)))
	return loaded, nil
}

// DLQList returns current DLQ entries (for /reflect failed).
func (r *Runner) DLQList() ([]LessonJob, error) { return r.dlq.List() }

// DLQCount returns current DLQ size (for status displays).
func (r *Runner) DLQCount() int { return r.dlq.Count() }

// QueueDepth returns current in-memory queue depth.
func (r *Runner) QueueDepth() int { return r.queue.Len() }

// PendingSnapshot returns a copy of queued jobs (for debugging /
// /reflect listing — keeps internals private).
func (r *Runner) PendingSnapshot() []LessonJob { return r.queue.Snapshot() }

// DLQPurge removes a DLQ entry (for /reflect purge <id>).
func (r *Runner) DLQPurge(id JobID) error { return r.dlq.Remove(id) }

// DLQReplay moves a DLQ entry back to the active queue (for /reflect
// retry <id>). Resets Attempts to 0 so the retry policy starts fresh.
func (r *Runner) DLQReplay(ctx context.Context, id JobID) error {
	job, ok, err := r.dlq.Pop(id)
	if err != nil {
		return fmt.Errorf("lessonq runner: dlq pop: %w", err)
	}
	if !ok {
		return fmt.Errorf("lessonq runner: dlq entry %s not found", id)
	}
	job.Attempts = 0
	job.LastError = ""
	job.NextAttemptAt = time.Now()
	if err := r.wal.Append(job); err != nil {
		return fmt.Errorf("lessonq runner: replay wal append: %w", err)
	}
	if err := r.queue.Enqueue(ctx, job); err != nil && !errors.Is(err, ErrDuplicate) {
		return fmt.Errorf("lessonq runner: replay queue enqueue: %w", err)
	}
	return nil
}

// DrainAndShutdown signals workers to stop, waits up to timeout for
// in-flight jobs to finish, closes the queue and WAL. Any jobs still
// queued are left in the WAL and will be replayed on next boot.
func (r *Runner) DrainAndShutdown(timeout time.Duration) {
	r.startMu.Lock()
	if !r.started {
		r.startMu.Unlock()
		// Always close WAL/DLQ even when Start was never called.
		r.queue.Close()
		r.wal.Close()
		r.dlq.Close()
		return
	}
	r.started = false
	r.startMu.Unlock()

	// Stop Dequeue-ers first so no new work is picked up.
	r.queue.Close()

	// Cancel worker ctx so in-flight processors have a chance to
	// honor cancellation (LLM calls check ctx.Err()).
	if r.cancelWorkers != nil {
		r.cancelWorkers()
	}

	done := make(chan struct{})
	go func() {
		r.workerWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		r.logger.Warn("lessonq runner: shutdown timed out, some workers still running",
			zap.Duration("timeout", timeout))
	}
	r.wal.Close()
	r.dlq.Close()
	r.logger.Info("lessonq runner: shutdown complete")
}

// ─── worker loop ──────────────────────────────────────────────────────────

// workerLoop is the per-worker goroutine: dequeue → process → classify
// → update WAL/queue/DLQ. Exits on ctx cancel or queue close.
func (r *Runner) workerLoop(ctx context.Context, workerID int) {
	defer r.workerWG.Done()

	for {
		job, ok, err := r.queue.Dequeue(ctx)
		if err != nil {
			// ctx canceled, queue closed → clean exit.
			return
		}
		if !ok {
			return
		}
		r.processJob(ctx, job, workerID)
	}
}

// processJob runs the injected Processor under a bounded timeout and
// classifies the result.
func (r *Runner) processJob(ctx context.Context, job LessonJob, workerID int) {
	started := time.Now()
	job.Attempts++

	jobCtx, cancel := context.WithTimeout(ctx, r.cfg.PerJobTimeout)
	defer cancel()

	r.processorMu.RLock()
	processor := r.processor
	r.processorMu.RUnlock()
	if processor == nil {
		// Shouldn't happen — Start requires non-nil processor. Guard
		// against misuse: put the job back so a future Start can
		// process it.
		r.logger.Warn("lessonq runner: processor nil at dequeue, re-enqueueing",
			zap.String("job_id", string(job.ID)))
		_ = r.queue.Enqueue(ctx, job)
		return
	}

	res := safeProcess(jobCtx, processor, job, r.logger)
	elapsed := time.Since(started)

	if r.metrics != nil {
		r.metrics.AttemptsTotal.WithLabelValues(res.Outcome.String()).Inc()
		r.metrics.ProcessingDuration.
			WithLabelValues(res.Outcome.String()).
			Observe(elapsed.Seconds())
	}

	switch res.Outcome {
	case OutcomeSuccess, OutcomeSkipped:
		if err := r.wal.Ack(job.ID); err != nil {
			r.logger.Warn("lessonq runner: wal ack failed",
				zap.String("job_id", string(job.ID)), zap.Error(err))
		}
	case OutcomeTransient:
		r.scheduleRetry(ctx, job, res)
	case OutcomePermanent:
		r.moveToDLQ(job, res)
	}
}

// scheduleRetry either re-enqueues with back-off (if attempts remain)
// or moves to DLQ.
func (r *Runner) scheduleRetry(ctx context.Context, job LessonJob, res ProcessResult) {
	if !r.cfg.Retry.ShouldRetry(job.Attempts) {
		// Exhausted — DLQ.
		r.logger.Warn("lessonq runner: retries exhausted, moving to DLQ",
			zap.String("job_id", string(job.ID)),
			zap.Int("attempts", job.Attempts))
		r.moveToDLQ(job, res)
		return
	}
	r.rngMu.Lock()
	delay := r.cfg.Retry.NextDelay(job.Attempts, r.rng)
	r.rngMu.Unlock()

	job.NextAttemptAt = time.Now().Add(delay)
	if res.Err != nil {
		job.LastError = res.Err.Error()
	}
	if r.metrics != nil {
		r.metrics.RetryTotal.
			WithLabelValues(fmt.Sprintf("%d", job.Attempts)).
			Inc()
	}
	if err := r.wal.Update(job); err != nil {
		r.logger.Warn("lessonq runner: wal update on retry failed",
			zap.String("job_id", string(job.ID)), zap.Error(err))
	}
	if err := r.queue.Enqueue(ctx, job); err != nil && !errors.Is(err, ErrDuplicate) {
		r.logger.Warn("lessonq runner: re-enqueue on retry failed",
			zap.String("job_id", string(job.ID)), zap.Error(err))
	}
}

// moveToDLQ writes the job to the DLQ WAL and acks the active WAL.
func (r *Runner) moveToDLQ(job LessonJob, res ProcessResult) {
	if res.Err != nil {
		job.LastError = res.Err.Error()
	}
	if err := r.dlq.Put(job); err != nil {
		r.logger.Warn("lessonq runner: dlq put failed",
			zap.String("job_id", string(job.ID)), zap.Error(err))
		// Best effort: keep the record in active WAL. It will get
		// retried next boot (possibly into DLQ again), not ideal but
		// never silent data loss.
		return
	}
	if err := r.wal.Ack(job.ID); err != nil {
		r.logger.Warn("lessonq runner: wal ack after dlq move failed",
			zap.String("job_id", string(job.ID)), zap.Error(err))
	}
}

// safeProcess wraps the caller-provided Processor in a panic recovery
// so a bad Processor doesn't take down the worker. A panic is
// classified as Permanent — panics are bugs, retrying just loops.
func safeProcess(ctx context.Context, p Processor, job LessonJob, logger *zap.Logger) (res ProcessResult) {
	defer func() {
		if rec := recover(); rec != nil {
			logger.Error("lessonq runner: processor panicked",
				zap.String("job_id", string(job.ID)),
				zap.Any("panic", rec),
				zap.Stack("stack"))
			res = ProcessResult{
				Outcome: OutcomePermanent,
				Err:     fmt.Errorf("processor panic: %v", rec),
			}
		}
	}()
	return p(ctx, job)
}
