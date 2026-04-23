/*
 * ChatCLI - Scheduler: core engine.
 *
 * Scheduler is the single object callers interact with. It composes the
 * WAL + in-memory queue + rate limiter + breaker group + registries
 * into a coherent lifecycle:
 *
 *   sched := scheduler.New(cfg, bridge, logger)
 *   scheduler.RegisterBuiltins(sched)           // standard plug-ins
 *   sched.Start(ctx)                            // spin up loops
 *   id, _ := sched.Enqueue(ctx, job)
 *   // ...
 *   sched.DrainAndShutdown(30 * time.Second)
 *
 * The worker model is two-tier:
 *
 *   Schedule pump — one goroutine owns the scheduleQueue. It PopReady's
 *   the earliest fireAt and hands the JobID to a bounded worker pool.
 *
 *   Worker pool  — N goroutines (Config.WorkerCount) pick up job IDs
 *   and run handleJob which walks the full Wait → Action → Finalize
 *   pipeline. Wait polls spawn their own ticker; the worker blocks on
 *   ctx and the poll channel.
 *
 * Durability contract:
 *   1. Every Enqueue fsyncs the WAL before returning.
 *   2. Every state transition Writes the updated Job to the WAL.
 *   3. Terminal jobs stay on disk for Config.DefaultTTL; the GC
 *      goroutine Acks them after expiration.
 */
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/diillson/chatcli/cli/bus"
	"github.com/diillson/chatcli/cli/hooks"
	"go.uber.org/zap"
)

// Scheduler is the top-level engine. Safe for concurrent use.
type Scheduler struct {
	cfg     Config
	logger  *zap.Logger
	bridge  CLIBridge
	metrics *Metrics

	// Persistence.
	wal   *schedulerWAL
	audit auditWriter

	// Scheduling.
	queue   *scheduleQueue
	rateLim *rateLimiter

	// Registries (plug-ins).
	conditions *ConditionRegistry
	actions    *ActionRegistry

	// Breakers.
	condBreakers *breakerGroup
	actBreakers  *breakerGroup

	// Live job table (ID → *Job). The WAL is the truth on disk; jobs
	// is its in-memory projection. Clones are handed to callers to
	// prevent accidental mutation.
	mu       sync.RWMutex
	jobs     map[JobID]*Job
	byName   map[string]JobID // name → live (non-terminal) JobID
	version  atomic.Uint64    // bumped on every mutation; used by UI subscribers to poll cheaply.

	// Event bus (optional — nil in headless tests).
	eventBus *bus.MessageBus
	// hookManager is optional — nil if hooks are disabled.
	hookManager *hooks.Manager

	// Lifecycle.
	startMu     sync.Mutex
	started     bool
	ctx         context.Context
	cancel      context.CancelFunc
	workers     sync.WaitGroup
	pumpDone    chan struct{}
	snapDone    chan struct{}
	gcDone      chan struct{}
	workCh      chan JobID
	shutdownMu  sync.Mutex
	draining    atomic.Bool
	closed      atomic.Bool
	rngMu       sync.Mutex
	rng         *rand.Rand
}

// SchedulerDeps bundles the optional external dependencies Scheduler
// uses for observability and coordination. All fields are optional; a
// nil value means "skip that channel".
type SchedulerDeps struct {
	EventBus *bus.MessageBus
	Hooks    *hooks.Manager
}

// New constructs a Scheduler from cfg + bridge. Call RegisterBuiltins
// to install the standard evaluators/executors, then Start to run.
//
// bridge may be nil; a noopBridge is substituted so the scheduler still
// boots (useful for the daemon's bootstrap phase before a CLI attaches).
func New(cfg Config, bridge CLIBridge, deps SchedulerDeps, logger *zap.Logger) (*Scheduler, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	if bridge == nil {
		bridge = NewNoopBridge()
	}
	if cfg.DataDir == "" {
		home, _ := os.UserHomeDir()
		cfg.DataDir = filepath.Join(home, ".chatcli", "scheduler")
	}
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return nil, fmt.Errorf("scheduler: mkdir data dir: %w", err)
	}

	metrics := GetMetrics()

	walDir := filepath.Join(cfg.DataDir, "wal")
	wal, err := newSchedulerWAL(walDir, logger)
	if err != nil {
		return nil, fmt.Errorf("scheduler: open wal: %w", err)
	}

	var audit auditWriter = NopAuditWriter()
	if cfg.AuditEnabled {
		audit = NewAuditFileWriter(cfg.DataDir, cfg.AuditMaxSizeMB, cfg.AuditMaxBackups, cfg.AuditMaxAgeDays, logger, metrics)
	}

	s := &Scheduler{
		cfg:         cfg,
		logger:      logger,
		bridge:      bridge,
		metrics:     metrics,
		wal:         wal,
		audit:       audit,
		queue:       newScheduleQueue(),
		rateLim:     newRateLimiter(cfg.RateLimitGlobalRPS, cfg.RateLimitOwnerRPS, cfg.RateLimitGlobalBurst, cfg.RateLimitOwnerBurst),
		conditions:  NewConditionRegistry(),
		actions:     NewActionRegistry(),
		jobs:        make(map[JobID]*Job),
		byName:      make(map[string]JobID),
		eventBus:    deps.EventBus,
		hookManager: deps.Hooks,
		rng:         rand.New(rand.NewSource(time.Now().UnixNano())), // #nosec G404 -- jitter, not security
	}

	// Breaker group is wired after `s` exists so onStateChange can
	// reach Scheduler.emit / metrics.
	s.condBreakers = newBreakerGroup(cfg.BreakerConfig, s.onBreakerChange("condition"))
	s.actBreakers = newBreakerGroup(cfg.BreakerConfig, s.onBreakerChange("action"))

	return s, nil
}

// Conditions returns the condition registry so callers may add
// extensions before Start.
func (s *Scheduler) Conditions() *ConditionRegistry { return s.conditions }

// Actions returns the action registry.
func (s *Scheduler) Actions() *ActionRegistry { return s.actions }

// Config returns a copy of the current runtime configuration.
func (s *Scheduler) Config() Config { return s.cfg }

// Metrics returns the Prometheus surface.
func (s *Scheduler) Metrics() *Metrics { return s.metrics }

// Bridge returns the wired CLIBridge (may be the noop one).
func (s *Scheduler) Bridge() CLIBridge { return s.bridge }

// SetBridge swaps the CLIBridge. Used by daemon mode when a CLI attaches.
// Safe to call while the scheduler is running.
func (s *Scheduler) SetBridge(b CLIBridge) {
	if b == nil {
		b = NewNoopBridge()
	}
	s.mu.Lock()
	s.bridge = b
	s.mu.Unlock()
}

// Version returns an opaque counter that bumps on every mutation.
// UI subscribers poll Version cheaply to decide whether to rerender.
func (s *Scheduler) Version() uint64 { return s.version.Load() }

// Snapshot triggers an immediate on-disk snapshot. Exposed for /jobs
// gc and daemon IPC "snapshot" calls.
func (s *Scheduler) Snapshot() error { return s.writeSnapshotNow() }

// ─── Start / Stop ────────────────────────────────────────────

// Start spins up the schedule pump, the worker pool, the snapshot and
// GC goroutines. Idempotent — a second Start is a no-op.
func (s *Scheduler) Start(ctx context.Context) error {
	s.startMu.Lock()
	defer s.startMu.Unlock()
	if s.started {
		return nil
	}
	if s.closed.Load() {
		return ErrSchedulerClosed
	}

	// Replay the WAL to hydrate the in-memory table and the scheduling
	// queue. If a snapshot exists we prefer it for boot speed.
	if err := s.replay(); err != nil {
		s.logger.Warn("scheduler: replay reported errors", zap.Error(err))
	}

	loopCtx, cancel := context.WithCancel(ctx)
	s.ctx = loopCtx
	s.cancel = cancel

	s.workCh = make(chan JobID, s.cfg.WorkerCount*2)
	s.pumpDone = make(chan struct{})
	s.snapDone = make(chan struct{})
	s.gcDone = make(chan struct{})

	// Schedule pump.
	go s.schedulePump()

	// Worker pool.
	for i := 0; i < s.cfg.WorkerCount; i++ {
		s.workers.Add(1)
		go s.workerLoop(i)
	}

	// Snapshot goroutine.
	go s.snapshotLoop()

	// GC goroutine.
	go s.gcLoop()

	s.started = true

	s.emit(NewEvent(EventDaemonStarted).WithMessage("scheduler started").
		WithData("workers", s.cfg.WorkerCount).
		WithData("data_dir", s.cfg.DataDir))

	s.logger.Info("scheduler: started",
		zap.Int("workers", s.cfg.WorkerCount),
		zap.String("data_dir", s.cfg.DataDir),
	)
	return nil
}

// DrainAndShutdown stops accepting new Enqueues, waits for in-flight
// work (up to timeout), then closes the queue + WAL + audit. Pending
// jobs remain in the WAL and will be replayed on next Start.
func (s *Scheduler) DrainAndShutdown(timeout time.Duration) {
	s.shutdownMu.Lock()
	defer s.shutdownMu.Unlock()

	if !s.started {
		// Always close resources even when Start was never called.
		s.closeResources()
		return
	}
	s.draining.Store(true)

	s.emit(NewEvent(EventDaemonStopped).WithMessage("scheduler draining"))

	// Take a final snapshot before stopping so boot is fast next time.
	_ = s.writeSnapshotNow()

	if s.cancel != nil {
		s.cancel()
	}
	s.queue.Close()

	done := make(chan struct{})
	go func() {
		s.workers.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		s.logger.Warn("scheduler: shutdown timeout", zap.Duration("timeout", timeout))
	}
	<-s.pumpDone
	<-s.snapDone
	<-s.gcDone

	s.closeResources()
	s.closed.Store(true)
	s.started = false
	s.logger.Info("scheduler: shutdown complete")
}

func (s *Scheduler) closeResources() {
	if s.wal != nil {
		s.wal.Close()
	}
	if s.audit != nil {
		_ = s.audit.Close()
	}
}

// IsClosed reports whether the scheduler has been shut down.
func (s *Scheduler) IsClosed() bool { return s.closed.Load() }

// ─── Enqueue / Mutations ─────────────────────────────────────

// Enqueue admits a new job. On success the returned Job is a clone of
// the one held inside the scheduler — callers may inspect but not
// mutate.
func (s *Scheduler) Enqueue(ctx context.Context, job *Job) (*Job, error) {
	if s.closed.Load() {
		return nil, ErrSchedulerClosed
	}
	if s.draining.Load() {
		return nil, ErrSchedulerDraining
	}
	if job == nil {
		return nil, fmt.Errorf("%w: nil job", ErrInvalidJob)
	}

	// Owner / action allowlist.
	if !s.cfg.AllowAgents && (job.Owner.Kind == OwnerAgent || job.Owner.Kind == OwnerWorker) {
		s.metrics.EnqueueErrors.WithLabelValues("agents_disabled").Inc()
		return nil, fmt.Errorf("%w: agent/worker scheduling disabled", ErrNotAuthorized)
	}
	if s.cfg.ActionAllowlist != nil && !s.cfg.ActionAllowlist[job.Action.Type] {
		s.metrics.EnqueueErrors.WithLabelValues("action_disallowed").Inc()
		return nil, fmt.Errorf("%w: action=%s", ErrActionDisallowed, job.Action.Type)
	}

	// Rate limit (outside the job mutex).
	if allowed, retry := s.rateLim.Allow(job.Owner); !allowed {
		s.metrics.EnqueueErrors.WithLabelValues("rate_limited").Inc()
		return nil, fmt.Errorf("%w: retry after %s", ErrRateLimited, retry)
	}

	// Fill defaults.
	if job.ID.IsZero() {
		nonce := job.CreatedAt.UTC().Format(time.RFC3339Nano)
		if nonce == "" {
			nonce = time.Now().UTC().Format(time.RFC3339Nano)
		}
		job.ID = DeriveJobID(job.Name, job.Owner, nonce)
	}
	if job.CreatedAt.IsZero() {
		job.CreatedAt = time.Now()
	}
	if job.UpdatedAt.IsZero() {
		job.UpdatedAt = job.CreatedAt
	}
	if job.Version == 0 {
		job.Version = SchemaVersion
	}
	job.Budget = job.Budget.Merge(s.cfg.budgetDefaults())
	if job.TTL <= 0 {
		job.TTL = s.cfg.DefaultTTL
	}
	if job.HistoryLimit <= 0 {
		job.HistoryLimit = s.cfg.HistoryLimit
	}

	// Validate.
	if err := job.Validate(); err != nil {
		s.metrics.EnqueueErrors.WithLabelValues("invalid").Inc()
		return nil, err
	}

	// Capacity and uniqueness.
	s.mu.Lock()
	if len(s.jobs) >= s.cfg.MaxJobs {
		s.mu.Unlock()
		s.metrics.EnqueueErrors.WithLabelValues("full").Inc()
		return nil, ErrQueueFull
	}
	if existing, has := s.byName[job.Name]; has {
		// Idempotent re-submit: if the existing job matches (same ID)
		// we return it; otherwise reject the duplicate name.
		if existing == job.ID {
			s.mu.Unlock()
			cur := s.jobs[existing]
			return cur.Clone(), nil
		}
		if cur, ok := s.jobs[existing]; ok && !cur.Status.IsTerminal() {
			s.mu.Unlock()
			s.metrics.EnqueueErrors.WithLabelValues("duplicate_name").Inc()
			return nil, fmt.Errorf("%w: %q is live as %s", ErrDuplicateName, job.Name, existing)
		}
	}
	// DAG cycle check.
	if len(job.DependsOn) > 0 {
		if err := s.validateNoCycleLocked(job); err != nil {
			s.mu.Unlock()
			return nil, err
		}
	}

	// Compute initial NextFireAt.
	next := job.Schedule.Next(time.Now(), job.CreatedAt)
	if next.IsZero() {
		// Absolute schedule already past — honor miss policy.
		switch job.Schedule.MissPolicy {
		case MissSkip:
			job.Status = StatusSkipped
		default:
			// fire_once and fire_all — schedule immediately.
			next = time.Now()
		}
	}
	job.NextFireAt = next

	// Initial status adjustment.
	if len(job.DependsOn) > 0 && job.Status == StatusPending {
		// Are any deps still non-terminal?
		for _, depID := range job.DependsOn {
			if d, ok := s.jobs[depID]; ok && !d.Status.IsTerminal() {
				job.Status = StatusBlocked
				break
			}
		}
	}

	// Persist + register.
	if err := s.wal.Write(job); err != nil {
		s.mu.Unlock()
		s.metrics.EnqueueErrors.WithLabelValues("wal").Inc()
		return nil, fmt.Errorf("scheduler: wal: %w", err)
	}
	s.jobs[job.ID] = job
	s.byName[job.Name] = job.ID
	s.mu.Unlock()

	if job.Status == StatusPending && !job.NextFireAt.IsZero() {
		s.queue.Enqueue(job.ID, job.NextFireAt)
	}

	s.metrics.JobsCreated.WithLabelValues(string(job.Owner.Kind), string(job.Action.Type)).Inc()
	s.metrics.ActiveJobs.Set(float64(s.activeCount()))
	s.metrics.QueueDepth.Set(float64(s.queue.Len()))
	s.version.Add(1)

	s.emit(NewEvent(EventJobCreated).WithJob(job).
		WithMessage(fmt.Sprintf("job %q created (status=%s)", job.Name, job.Status)).
		WithData("action", string(job.Action.Type)))

	clone := job.Clone()
	return clone, nil
}

// Cancel marks the job cancelled. Running jobs are notified via ctx
// cancellation (their action executor sees ctx.Done() fire).
//
// Locking discipline: the job lock is held only during the mutation;
// emit() is invoked outside the lock because Event.WithJob reacquires
// the same mutex to snapshot job fields and would otherwise deadlock.
func (s *Scheduler) Cancel(id JobID, reason string, requester Owner) error {
	s.mu.Lock()
	j, ok := s.jobs[id]
	s.mu.Unlock()
	if !ok {
		return ErrJobNotFound
	}
	if !isAuthorized(requester, j.Owner) {
		return ErrNotAuthorized
	}
	j.lock()
	if j.Status.IsTerminal() {
		j.unlock()
		return ErrJobTerminal
	}
	if err := j.transition(StatusCancelled, reason, s.logger); err != nil {
		j.unlock()
		return err
	}
	j.CancelReason = reason
	_ = s.wal.Write(j)
	j.unlock()

	s.queue.Remove(id)
	s.cleanupNameLocked(j)
	s.version.Add(1)
	s.emit(NewEvent(EventJobCancelled).WithJob(j).WithMessage(reason))
	s.metrics.ActiveJobs.Set(float64(s.activeCount()))
	return nil
}

// Pause halts a non-running job until Resume is called.
func (s *Scheduler) Pause(id JobID, reason string, requester Owner) error {
	s.mu.Lock()
	j, ok := s.jobs[id]
	s.mu.Unlock()
	if !ok {
		return ErrJobNotFound
	}
	if !isAuthorized(requester, j.Owner) {
		return ErrNotAuthorized
	}
	j.lock()
	if j.Status == StatusRunning {
		j.unlock()
		return fmt.Errorf("scheduler: cannot pause running job; cancel instead")
	}
	if err := j.transition(StatusPaused, reason, s.logger); err != nil {
		j.unlock()
		return err
	}
	j.PauseReason = reason
	_ = s.wal.Write(j)
	j.unlock()

	s.queue.Remove(id)
	s.emit(NewEvent(EventJobPaused).WithJob(j).WithMessage(reason))
	s.version.Add(1)
	return nil
}

// Resume takes a Paused job back to Pending and re-enqueues it.
func (s *Scheduler) Resume(id JobID, requester Owner) error {
	s.mu.Lock()
	j, ok := s.jobs[id]
	s.mu.Unlock()
	if !ok {
		return ErrJobNotFound
	}
	if !isAuthorized(requester, j.Owner) {
		return ErrNotAuthorized
	}
	j.lock()
	if err := j.transition(StatusPending, "resumed", s.logger); err != nil {
		j.unlock()
		return err
	}
	j.PauseReason = ""
	// Recompute next fire time.
	j.NextFireAt = j.Schedule.Next(time.Now(), j.CreatedAt)
	if j.NextFireAt.IsZero() {
		j.NextFireAt = time.Now()
	}
	_ = s.wal.Write(j)
	fireAt := j.NextFireAt
	j.unlock()

	s.queue.Enqueue(id, fireAt)
	s.emit(NewEvent(EventJobResumed).WithJob(j))
	s.version.Add(1)
	return nil
}

// Query returns a cloned snapshot of the job (including history), or
// ErrJobNotFound.
func (s *Scheduler) Query(id JobID) (*Job, error) {
	s.mu.RLock()
	j, ok := s.jobs[id]
	s.mu.RUnlock()
	if !ok {
		return nil, ErrJobNotFound
	}
	return j.Clone(), nil
}

// FindByName returns the live (non-terminal) job for a name, if any.
func (s *Scheduler) FindByName(name string) (*Job, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.byName[name]
	if !ok {
		return nil, false
	}
	j, ok := s.jobs[id]
	if !ok {
		return nil, false
	}
	return j.Clone(), true
}

// ListFilter is an OR-of-conjunctions filter for List.
type ListFilter struct {
	Owner      *Owner
	Statuses   []JobStatus
	Tag        string
	NameSubstr string
	IncludeTerminal bool
}

// List returns summaries of every known job matching the filter.
// Defaults: non-terminal only, sorted by NextFireAt then CreatedAt.
func (s *Scheduler) List(filter ListFilter) []JobSummary {
	s.mu.RLock()
	jobs := make([]*Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		jobs = append(jobs, j)
	}
	s.mu.RUnlock()

	out := make([]JobSummary, 0, len(jobs))
	for _, j := range jobs {
		sum := j.Summary()
		if !filter.IncludeTerminal && sum.Status.IsTerminal() {
			continue
		}
		if filter.Owner != nil && !sum.Owner.Equal(*filter.Owner) {
			continue
		}
		if len(filter.Statuses) > 0 {
			match := false
			for _, st := range filter.Statuses {
				if sum.Status == st {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}
		if filter.Tag != "" {
			found := false
			for _, t := range sum.Tags {
				if strings.EqualFold(t, filter.Tag) || strings.HasPrefix(strings.ToLower(t), strings.ToLower(filter.Tag)+"=") {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		if filter.NameSubstr != "" && !strings.Contains(strings.ToLower(sum.Name), strings.ToLower(filter.NameSubstr)) {
			continue
		}
		out = append(out, sum)
	}
	// Sort: active/pending first (nearest fire first), then terminal (newest first).
	sortJobSummaries(out)
	return out
}

// ─── Pump + worker loop ──────────────────────────────────────

func (s *Scheduler) schedulePump() {
	defer close(s.pumpDone)
	for {
		id, ok, err := s.queue.PopReady(s.ctx)
		if err != nil {
			return
		}
		if !ok {
			return
		}
		// Dispatch with non-blocking send; if the pool is saturated,
		// back off briefly and retry. This preserves FIFO of dispatch
		// under load without requiring an unbounded channel.
		select {
		case s.workCh <- id:
		case <-s.ctx.Done():
			return
		default:
			// Requeue with a tiny delay and try again.
			s.mu.RLock()
			j, ok := s.jobs[id]
			s.mu.RUnlock()
			if ok {
				s.queue.Enqueue(id, time.Now().Add(50*time.Millisecond))
			} else {
				_ = j
			}
		}
	}
}

func (s *Scheduler) workerLoop(id int) {
	defer s.workers.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case jobID, ok := <-s.workCh:
			if !ok {
				return
			}
			s.handleJob(jobID, id)
		}
	}
}

// ─── Observability fan-out ───────────────────────────────────

// emit is the single point where transitions produce observability
// signals: metrics, audit log, event bus, hook manager.
func (s *Scheduler) emit(evt Event) {
	if evt.Timestamp.IsZero() {
		evt.Timestamp = time.Now()
	}
	// Audit.
	s.audit.Write(evt)

	// Bus.
	if s.eventBus != nil {
		msg := bus.OutboundMessage{
			ID:        string(evt.JobID),
			Channel:   "scheduler",
			Content:   string(evt.Type),
			Type:      bus.MessageTypeSystem,
			Timestamp: evt.Timestamp,
			Metadata: map[string]string{
				"event":  string(evt.Type),
				"job":    string(evt.JobID),
				"name":   evt.Name,
				"status": string(evt.Status),
			},
		}
		_ = s.eventBus.PublishOutbound(context.Background(), msg)
	}
	// Hooks.
	if s.hookManager != nil {
		hookEvt := hooks.HookEvent{
			Type:      hooks.EventType("Scheduler." + string(evt.Type)),
			Timestamp: evt.Timestamp,
			SessionID: string(evt.JobID),
			ToolName:  evt.Name,
			ToolArgs:  string(mustMarshal(evt.Data)),
		}
		if evt.Message != "" {
			hookEvt.ToolOutput = evt.Message
		}
		s.hookManager.FireAsync(hookEvt)
	}
	// Bridge (so the live cli Ctrl+J overlay can redraw).
	if s.bridge != nil {
		s.bridge.PublishEvent(evt)
	}
}

func (s *Scheduler) onBreakerChange(kind string) func(string, BreakerState, BreakerState) {
	return func(key string, from, to BreakerState) {
		if s.metrics != nil {
			s.metrics.BreakerState.WithLabelValues(kind, key).Set(float64(to))
		}
		var ev EventType
		switch to {
		case BreakerOpen:
			ev = EventBreakerOpened
		case BreakerClosed:
			ev = EventBreakerClosed
		case BreakerHalfOpen:
			ev = EventBreakerHalfOpen
		default:
			return
		}
		s.emit(NewEvent(ev).
			WithMessage(fmt.Sprintf("breaker %s:%s %s→%s", kind, key, from, to)).
			WithData("kind", kind).WithData("key", key))
	}
}

// ─── helpers ──────────────────────────────────────────────────

func (s *Scheduler) activeCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, j := range s.jobs {
		if !j.Status.IsTerminal() {
			n++
		}
	}
	return n
}

func (s *Scheduler) cleanupNameLocked(j *Job) {
	s.mu.Lock()
	if id, ok := s.byName[j.Name]; ok && id == j.ID {
		delete(s.byName, j.Name)
	}
	s.mu.Unlock()
}

// validateNoCycleLocked assumes s.mu is held.
func (s *Scheduler) validateNoCycleLocked(j *Job) error {
	seen := map[JobID]bool{j.ID: true}
	var walk func(id JobID) error
	walk = func(id JobID) error {
		if seen[id] {
			return ErrDAGCycle
		}
		seen[id] = true
		dep, ok := s.jobs[id]
		if !ok {
			return nil
		}
		for _, next := range dep.DependsOn {
			if err := walk(next); err != nil {
				return err
			}
		}
		return nil
	}
	for _, d := range j.DependsOn {
		if err := walk(d); err != nil {
			return err
		}
	}
	return nil
}

// isAuthorized decides whether requester may mutate job with owner.
// Rules:
//   - Same owner always wins.
//   - OwnerSystem and OwnerUser may touch anything.
//   - Agents may only cancel their own or their subordinate workers'.
func isAuthorized(requester, owner Owner) bool {
	if requester.Kind == OwnerSystem || requester.Kind == OwnerUser {
		return true
	}
	if requester.Equal(owner) {
		return true
	}
	if requester.Kind == OwnerAgent && owner.Kind == OwnerWorker && requester.ID == owner.Tag {
		// Agent spawned this worker; allowed.
		return true
	}
	return false
}

// sortJobSummaries orders by (non-terminal first, nearest fire asc,
// terminal newest first).
func sortJobSummaries(in []JobSummary) {
	byTerm := func(s JobSummary) int {
		if s.Status.IsTerminal() {
			return 1
		}
		return 0
	}
	// Insertion sort keeps stable ordering for same-key entries.
	for i := 1; i < len(in); i++ {
		for j := i; j > 0; j-- {
			a, b := in[j-1], in[j]
			at, bt := byTerm(a), byTerm(b)
			if at < bt {
				break
			}
			if at == bt {
				if at == 0 {
					// Active: earlier NextFireAt first.
					if !a.NextFireAt.IsZero() && !b.NextFireAt.IsZero() {
						if a.NextFireAt.Before(b.NextFireAt) {
							break
						}
						if b.NextFireAt.Before(a.NextFireAt) {
							in[j-1], in[j] = in[j], in[j-1]
							continue
						}
					}
					// Fallback: creation order.
					if a.CreatedAt.Before(b.CreatedAt) {
						break
					}
					in[j-1], in[j] = in[j], in[j-1]
					continue
				}
				// Terminal: newest (UpdatedAt desc) first.
				if a.UpdatedAt.After(b.UpdatedAt) {
					break
				}
				in[j-1], in[j] = in[j], in[j-1]
				continue
			}
			in[j-1], in[j] = in[j], in[j-1]
		}
	}
}

// Guard against silently discarding errors in future refactors.
var _ = errors.New
