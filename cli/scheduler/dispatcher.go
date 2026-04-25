/*
 * ChatCLI - Scheduler: per-job dispatch logic (wait → action → finalize).
 *
 * handleJob is the heart of the scheduler. For a single fire:
 *
 *   1. Load the Job and verify it is still eligible (not cancelled
 *      between pump and worker dequeue).
 *
 *   2. If Wait is set, enter the wait loop: poll Condition every
 *      PollInterval until satisfied, timed out, or budget exhausted.
 *      Each poll result is metrics-emitted and the breaker is updated.
 *
 *   3. Run the Action under a bounded timeout. Outcome classification
 *      drives the next transition (success → Completed, transient
 *      failure → retry-with-backoff, permanent → Failed).
 *
 *   4. If the job has Triggers edges, spawn the children and mark them
 *      ready. Children created from a Triggers edge get a fresh ID
 *      (NewJobID) so a recurring parent can fan out a fresh DAG on
 *      every cycle.
 *
 *   5. Recurring schedules (cron / interval) re-enqueue with the
 *      freshly computed next time unless the job itself is terminal.
 */
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"
)

// handleJob drives one fire of the given job ID.
func (s *Scheduler) handleJob(id JobID, workerID int) {
	s.mu.RLock()
	j, ok := s.jobs[id]
	s.mu.RUnlock()
	if !ok {
		return
	}

	// Cheap check without taking the job lock; handleJob takes it below.
	if j.Status.IsTerminal() || j.Status == StatusPaused {
		return
	}

	s.logger.Debug("scheduler: handling job",
		zap.String("job_id", string(id)),
		zap.String("name", j.Name),
		zap.Int("worker_id", workerID),
	)

	// Emit "fired" and run the wait + action pipeline.
	s.emit(NewEvent(EventJobFired).WithJob(j).WithMessage("job fire window reached"))

	// Wait phase (if any).
	if j.Wait != nil {
		if !s.runWait(j) {
			return
		}
	}

	// Action phase.
	s.runAction(j)

	// Recurring re-enqueue (outside the action section so a terminal
	// transition by the action doesn't double-schedule).
	s.maybeReschedule(j)
}

// ─── Wait phase ───────────────────────────────────────────────

// runWait polls the condition until satisfied, timed out, or cancelled.
// Returns true iff the caller should proceed to Action.
func (s *Scheduler) runWait(j *Job) bool {
	j.lock()
	if j.Status.IsTerminal() {
		j.unlock()
		return false
	}
	if err := j.transition(StatusWaiting, "begin wait", s.logger); err != nil {
		j.unlock()
		return false
	}
	_ = s.wal.Write(j)
	wait := *j.Wait
	budget := j.Budget
	startAt := time.Now()
	j.unlock()

	s.emit(NewEvent(EventJobWaitStarted).WithJob(j).WithData("condition_type", wait.Condition.Type))

	// Resolve evaluator.
	eval, ok := s.conditions.Get(wait.Condition.Type)
	if !ok {
		s.failJob(j, fmt.Errorf("%w: unknown condition type %q", ErrInvalidCondition, wait.Condition.Type), false)
		return false
	}

	pollInterval := budget.PollInterval
	if pollInterval <= 0 {
		pollInterval = s.cfg.DefaultPollInterval
	}
	timeout := budget.WaitTimeout
	if timeout <= 0 {
		timeout = s.cfg.DefaultWaitTimeout
	}
	maxPolls := budget.MaxPolls // 0 = unlimited

	waitCtx, cancel := context.WithTimeout(s.ctx, timeout)
	defer cancel()

	attempt := 0
	for {
		if waitCtx.Err() != nil {
			// Timeout.
			return s.waitTimeout(j, wait, startAt, attempt)
		}
		// Cancellation check — user may have cancelled the job.
		j.lock()
		cancelled := j.Status == StatusCancelled
		j.unlock()
		if cancelled {
			return false
		}

		attempt++
		pollCtx, pollCancel := context.WithTimeout(waitCtx, pollInterval*3)
		br := s.condBreakers.Get(wait.Condition.Type)
		release, bErr := br.Acquire()
		if bErr != nil {
			// Breaker open — count as failed poll but don't over-slam.
			release(false)
			s.metrics.WaitChecks.WithLabelValues(wait.Condition.Type, "breaker_open").Inc()
			pollCancel()
			select {
			case <-time.After(pollInterval):
			case <-waitCtx.Done():
			}
			if maxPolls > 0 && attempt >= maxPolls {
				return s.waitTimeout(j, wait, startAt, attempt)
			}
			continue
		}

		pollStart := time.Now()
		out := eval.Evaluate(pollCtx, wait.Condition, &EvalEnv{Logger: s.logger, Bridge: s.bridge, DangerousConfirmed: j.DangerousConfirmed})
		pollCancel()
		pollDur := time.Since(pollStart)
		s.metrics.WaitDuration.WithLabelValues(wait.Condition.Type).Observe(pollDur.Seconds())

		if wait.Condition.Negate {
			out.Satisfied = !out.Satisfied
		}

		satisfiedLabel := "false"
		if out.Satisfied {
			satisfiedLabel = "true"
		}
		if out.Err != nil {
			satisfiedLabel = "error"
		}
		s.metrics.WaitChecks.WithLabelValues(wait.Condition.Type, satisfiedLabel).Inc()

		// Breaker feedback: non-transient error counts as a failure.
		release(out.Err == nil || out.Transient)

		// Record attempt on the job.
		j.lock()
		j.recordExecution(ExecutionResult{
			AttemptNum:         attempt,
			StartedAt:          pollStart,
			FinishedAt:         pollStart.Add(pollDur),
			Duration:           pollDur,
			Outcome:            conditionOutcome(out),
			Output:             truncate(out.Details, 4096),
			Error:              errString(out.Err),
			ConditionSatisfied: out.Satisfied,
			ConditionDetails:   out.Details,
		})
		_ = s.wal.Write(j)
		j.unlock()

		s.emit(NewEvent(EventJobWaitTick).WithJob(j).
			WithData("attempt", attempt).
			WithData("satisfied", out.Satisfied).
			WithData("details", truncate(out.Details, 200)))

		if out.Satisfied {
			s.emit(NewEvent(EventJobWaitSatisfied).WithJob(j).
				WithData("attempt", attempt).
				WithData("duration", time.Since(startAt).String()))
			return true
		}

		if out.Err != nil && !out.Transient {
			s.failJob(j, fmt.Errorf("wait evaluator %q: %w", wait.Condition.Type, out.Err), false)
			return false
		}

		// Non-satisfied, permissible — wait for the next tick.
		if maxPolls > 0 && attempt >= maxPolls {
			return s.waitTimeout(j, wait, startAt, attempt)
		}
		select {
		case <-time.After(pollInterval):
		case <-waitCtx.Done():
		}
	}
}

// waitTimeout applies the TimeoutBehavior policy. Returns true if the
// caller should still run the Action (TimeoutFireAnyway).
func (s *Scheduler) waitTimeout(j *Job, wait WaitSpec, startedAt time.Time, attempts int) bool {
	behavior := wait.OnTimeout
	if behavior == "" {
		behavior = TimeoutFail
	}
	s.emit(NewEvent(EventJobTimedOut).WithJob(j).
		WithData("attempts", attempts).
		WithData("elapsed", time.Since(startedAt).String()).
		WithData("behavior", string(behavior)))

	switch behavior {
	case TimeoutFireAnyway:
		return true
	case TimeoutFallback:
		if wait.Fallback != nil {
			// Clone to avoid leaking the pointer into the executor.
			j.lock()
			origAction := j.Action
			j.Action = *wait.Fallback
			j.unlock()
			s.runAction(j)
			j.lock()
			j.Action = origAction
			j.unlock()
		}
		s.failJob(j, ErrWaitTimeout, true)
		return false
	default:
		s.failJob(j, ErrWaitTimeout, true)
		return false
	}
}

// ─── Action phase ─────────────────────────────────────────────

func (s *Scheduler) runAction(j *Job) {
	j.lock()
	if j.Status.IsTerminal() {
		j.unlock()
		return
	}
	if err := j.transition(StatusRunning, "begin action", s.logger); err != nil {
		j.unlock()
		return
	}
	j.Attempts++
	attempt := j.Attempts
	action := j.Action
	budget := j.Budget
	_ = s.wal.Write(j)
	j.unlock()

	s.emit(NewEvent(EventJobRunning).WithJob(j).WithData("action_type", string(action.Type)))

	// Resolve executor.
	execFn, ok := s.actions.Get(action.Type)
	if !ok {
		s.failJob(j, fmt.Errorf("%w: unknown action type %q", ErrInvalidAction, action.Type), false)
		return
	}

	// Bounded context.
	timeout := budget.ActionTimeout
	if timeout <= 0 {
		timeout = s.cfg.DefaultActionTimeout
	}
	execCtx, cancel := context.WithTimeout(s.ctx, timeout)
	defer cancel()

	br := s.actBreakers.Get(string(action.Type))
	release, bErr := br.Acquire()
	if bErr != nil {
		release(false)
		s.recordActionResult(j, attempt, ExecutionResult{
			AttemptNum: attempt,
			StartedAt:  time.Now(),
			FinishedAt: time.Now(),
			Outcome:    OutcomeBreakerOff,
			Error:      bErr.Error(),
		})
		s.scheduleRetryOrFail(j, bErr)
		return
	}

	startedAt := time.Now()
	env := &ExecEnv{Logger: s.logger, Bridge: s.bridge, Job: j.Summary()}
	res := execFn.Execute(execCtx, action, env)
	duration := time.Since(startedAt)
	release(res.Err == nil)

	var outcome Outcome
	switch {
	case res.Err == nil:
		outcome = OutcomeSuccess
	case errors.Is(res.Err, context.DeadlineExceeded):
		outcome = OutcomeTimeout
	case errors.Is(res.Err, context.Canceled):
		outcome = OutcomeCancelled
	default:
		outcome = OutcomeFailed
	}

	s.metrics.ActionDuration.WithLabelValues(string(action.Type), string(outcome)).Observe(duration.Seconds())

	exec := ExecutionResult{
		AttemptNum: attempt,
		StartedAt:  startedAt,
		FinishedAt: startedAt.Add(duration),
		Duration:   duration,
		Outcome:    outcome,
		Output:     truncate(res.Output, 1<<15),
		Error:      errString(res.Err),
		Tokens:     res.Tokens,
		Cost:       res.Cost,
	}
	s.recordActionResult(j, attempt, exec)

	if res.Err == nil {
		s.markCompleted(j, exec)
		return
	}

	// Decide retry vs fail.
	if res.Transient && shouldRetry(budget, attempt) {
		s.scheduleRetryOrFail(j, res.Err)
		return
	}
	s.failJob(j, res.Err, false)
}

// ─── Finalization helpers ────────────────────────────────────

func (s *Scheduler) recordActionResult(j *Job, attempt int, r ExecutionResult) {
	j.lock()
	j.recordExecution(r)
	_ = s.wal.Write(j)
	j.unlock()
	s.metrics.JobsFired.WithLabelValues(string(r.Outcome), string(j.Action.Type)).Inc()
	_ = attempt
}

func (s *Scheduler) markCompleted(j *Job, r ExecutionResult) {
	j.lock()
	if j.Status.IsTerminal() {
		j.unlock()
		return
	}
	_ = j.transition(StatusCompleted, "action succeeded", s.logger)
	_ = s.wal.Write(j)
	triggers := append([]JobID(nil), j.Triggers...)
	j.unlock()

	s.emit(NewEvent(EventJobCompleted).WithJob(j).WithExecution(r))
	s.version.Add(1)

	s.unblockDependents(j.ID)
	s.fireTriggers(j, triggers)
	s.cleanupNameLocked(j)
}

func (s *Scheduler) failJob(j *Job, err error, timedOut bool) {
	j.lock()
	if j.Status.IsTerminal() {
		j.unlock()
		return
	}
	target := StatusFailed
	if timedOut {
		target = StatusTimedOut
	}
	msg := errString(err)
	_ = j.transition(target, msg, s.logger)
	_ = s.wal.Write(j)
	j.unlock()

	var ev Event
	if timedOut {
		ev = NewEvent(EventJobTimedOut).WithJob(j).WithMessage(msg)
	} else {
		ev = NewEvent(EventJobFailed).WithJob(j).WithMessage(msg)
	}
	s.emit(ev)
	s.version.Add(1)
	s.cleanupNameLocked(j)
}

func (s *Scheduler) scheduleRetryOrFail(j *Job, err error) {
	j.lock()
	if j.Status.IsTerminal() {
		j.unlock()
		return
	}
	attempts := j.Attempts
	budget := j.Budget
	j.unlock()

	if !shouldRetry(budget, attempts) {
		s.failJob(j, fmt.Errorf("retries exhausted: %w", err), false)
		return
	}

	s.rngMu.Lock()
	delay := nextDelay(budget, attempts, s.rng)
	s.rngMu.Unlock()

	j.lock()
	// Back to Pending so the main loop re-fires after the delay.
	_ = j.transition(StatusPending, fmt.Sprintf("retry after %s", delay), s.logger)
	j.NextFireAt = time.Now().Add(delay)
	_ = s.wal.Write(j)
	j.unlock()

	s.queue.Enqueue(j.ID, j.NextFireAt)
	s.metrics.RetryCount.WithLabelValues(retryBucket(attempts)).Inc()
	s.emit(NewEvent(EventJobRetryQueued).WithJob(j).
		WithData("attempt", attempts).
		WithData("delay", delay.String()).
		WithMessage(errString(err)))
}

// maybeReschedule handles recurring schedules and DAG fan-out on the
// non-terminal path. Terminal jobs are left alone — completion
// handling already runs unblockDependents / fireTriggers.
func (s *Scheduler) maybeReschedule(j *Job) {
	j.lock()
	if j.Status.IsTerminal() {
		j.unlock()
		return
	}
	if j.Status != StatusCompleted {
		// Non-completion paths (retry, fail) manage their own re-enqueue.
		j.unlock()
		return
	}
	sched := j.Schedule
	createdAt := j.CreatedAt
	j.unlock()
	if !sched.IsRecurring() {
		return
	}
	next := sched.Next(time.Now(), createdAt)
	if next.IsZero() {
		return
	}
	j.lock()
	// Reset state for a fresh occurrence, but we stay on the same Job
	// record — the JobID + WAL record are reused. History carries the
	// prior executions.
	_ = j.transition(StatusPending, "recurring re-arm", s.logger)
	j.NextFireAt = next
	j.Attempts = 0
	_ = s.wal.Write(j)
	j.unlock()
	s.queue.Enqueue(j.ID, next)
}

// unblockDependents scans for any Blocked jobs that had this one in
// their DependsOn list; if all their deps are now terminal-successful,
// they become Pending. Event emission and queue enqueue happen after
// the dep lock is released — emit internally reacquires Job.mu and
// would otherwise deadlock.
func (s *Scheduler) unblockDependents(finished JobID) {
	s.mu.RLock()
	candidates := make([]*Job, 0)
	for _, j := range s.jobs {
		if j.Status != StatusBlocked {
			continue
		}
		for _, dep := range j.DependsOn {
			if dep == finished {
				candidates = append(candidates, j)
				break
			}
		}
	}
	s.mu.RUnlock()

	for _, dep := range candidates {
		outcome := depOutcome{}
		s.resolveDependency(dep, &outcome)

		switch outcome.kind {
		case depBecamePending:
			s.queue.Enqueue(dep.ID, outcome.fireAt)
			s.emit(NewEvent(EventJobDependency).WithJob(dep).WithMessage("dependencies resolved"))
			s.version.Add(1)
		case depFailed:
			s.emit(NewEvent(EventJobFailed).WithJob(dep).WithMessage(outcome.message))
			s.version.Add(1)
			s.cleanupNameLocked(dep)
		}
	}
}

// depOutcomeKind classifies the result of resolveDependency so
// emission stays outside the lock.
type depOutcomeKind int

const (
	depUnchanged depOutcomeKind = iota
	depBecamePending
	depFailed
)

// depOutcome bundles a resolution result.
type depOutcome struct {
	kind    depOutcomeKind
	fireAt  time.Time
	message string
}

// resolveDependency holds dep.lock while inspecting the DAG and
// possibly transitioning dep. Out-of-lock side effects are encoded in
// outcome so the caller can emit / enqueue safely.
func (s *Scheduler) resolveDependency(dep *Job, outcome *depOutcome) {
	dep.lock()
	defer dep.unlock()
	for _, d := range dep.DependsOn {
		s.mu.RLock()
		jj, ok := s.jobs[d]
		s.mu.RUnlock()
		if !ok {
			continue
		}
		// Snapshot the dependency's status safely.
		jj.lock()
		jjStatus := jj.Status
		jj.unlock()
		if !jjStatus.IsTerminal() {
			return
		}
		if jjStatus != StatusCompleted {
			if err := dep.transition(StatusFailed, fmt.Sprintf("dependency %s ended in %s", d, jjStatus), s.logger); err == nil {
				_ = s.wal.Write(dep)
				outcome.kind = depFailed
				outcome.message = "dependency failed"
			}
			return
		}
	}
	if err := dep.transition(StatusPending, "dependencies resolved", s.logger); err == nil {
		if dep.NextFireAt.IsZero() || dep.NextFireAt.Before(time.Now()) {
			dep.NextFireAt = time.Now()
		}
		_ = s.wal.Write(dep)
		outcome.kind = depBecamePending
		outcome.fireAt = dep.NextFireAt
	}
}

// fireTriggers spawns the children listed in triggers. Each child is a
// fresh clone of the referenced template with a new ID, scheduled to
// fire immediately.
func (s *Scheduler) fireTriggers(parent *Job, triggers []JobID) {
	for _, tid := range triggers {
		s.mu.RLock()
		template, ok := s.jobs[tid]
		s.mu.RUnlock()
		if !ok {
			continue
		}
		clone := template.Clone()
		clone.ID = NewJobID()
		clone.ParentID = parent.ID
		clone.Status = StatusPending
		clone.CreatedAt = time.Now()
		clone.UpdatedAt = clone.CreatedAt
		clone.FinishedAt = time.Time{}
		clone.NextFireAt = time.Now()
		clone.Attempts = 0
		clone.History = nil
		clone.Transitions = nil
		clone.LastResult = nil
		clone.Triggers = nil
		// Name must be unique among live jobs; append a short suffix.
		clone.Name = fmt.Sprintf("%s.t%d", template.Name, time.Now().UnixNano()%10000)
		if _, err := s.Enqueue(s.ctx, clone); err != nil {
			s.logger.Warn("scheduler: trigger enqueue failed",
				zap.String("parent", string(parent.ID)),
				zap.String("template", string(tid)),
				zap.Error(err))
		}
	}
}

// conditionOutcome maps an EvalOutcome to an Outcome for history.
func conditionOutcome(o EvalOutcome) Outcome {
	if o.Err != nil {
		if o.Transient {
			return OutcomeFailed
		}
		return OutcomeFailed
	}
	if o.Satisfied {
		return OutcomeSuccess
	}
	return OutcomeSkipped
}

// ─── small helpers ────────────────────────────────────────────

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return strings.TrimSpace(err.Error())
}
