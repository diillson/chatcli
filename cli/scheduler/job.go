/*
 * ChatCLI - Scheduler: Job struct + lifecycle primitives.
 *
 * A Job is the durable record the scheduler works on. It holds:
 *   - Identity       (ID, Name, Owner, Tags)
 *   - Schedule       (when to fire)
 *   - Action         (what to run)
 *   - WaitSpec       (optional gating condition)
 *   - Budget         (timeouts, retries, poll limits)
 *   - DAG edges      (DependsOn, Triggers)
 *   - State          (Status + transitions history)
 *   - Results        (LastResult, History)
 *
 * The struct is serialized verbatim to the WAL (one record per job),
 * the audit log, and the IPC protocol. Schema migration is handled via
 * the Version field — new fields use omitempty so old records remain
 * readable.
 */
package scheduler

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// ensureSyncImport keeps the sync dependency used (mu field).
var _ = sync.Mutex{}

// SchemaVersion increments whenever Job gains a required field. Tests
// assert the WAL is forward-compatible via the /testdata fixtures.
const SchemaVersion = 1

// Job is the unit of scheduling. See the state machine in
// state_machine.go for legal transitions.
type Job struct {
	// ─── Identity ──────────────────────────────────────────
	ID      JobID             `json:"id"`
	Name    string            `json:"name"`
	Owner   Owner             `json:"owner"`
	Tags    map[string]string `json:"tags,omitempty"`
	Version int               `json:"version"`

	// ─── Scheduling ────────────────────────────────────────
	Schedule Schedule  `json:"schedule"`
	Action   Action    `json:"action"`
	Wait     *WaitSpec `json:"wait,omitempty"`

	// ─── DAG ───────────────────────────────────────────────
	DependsOn []JobID `json:"depends_on,omitempty"`
	Triggers  []JobID `json:"triggers,omitempty"`
	// ParentID is non-empty for jobs spawned by a Triggers edge, so
	// /jobs tree can render the provenance.
	ParentID JobID `json:"parent_id,omitempty"`

	// ─── Execution budget ──────────────────────────────────
	Budget Budget        `json:"budget,omitempty"`
	TTL    time.Duration `json:"ttl,omitempty"`

	// ─── State machine ─────────────────────────────────────
	Status       JobStatus          `json:"status"`
	NextFireAt   time.Time          `json:"next_fire_at,omitempty"`
	CreatedAt    time.Time          `json:"created_at"`
	UpdatedAt    time.Time          `json:"updated_at"`
	FinishedAt   time.Time          `json:"finished_at,omitempty"`
	Transitions  []TransitionReason `json:"transitions,omitempty"`
	PauseReason  string             `json:"pause_reason,omitempty"`
	CancelReason string             `json:"cancel_reason,omitempty"`

	// ─── Execution history ─────────────────────────────────
	LastResult   *ExecutionResult  `json:"last_result,omitempty"`
	History      []ExecutionResult `json:"history,omitempty"`
	HistoryLimit int               `json:"history_limit,omitempty"`
	Attempts     int               `json:"attempts,omitempty"`

	// ─── Flags ─────────────────────────────────────────────
	// DangerousConfirmed marks a job whose action is otherwise filtered
	// by the shell safety filter but the creator passed --i-know.
	DangerousConfirmed bool `json:"dangerous_confirmed,omitempty"`
	// Description is a free-form label shown in `/jobs show`.
	Description string `json:"description,omitempty"`

	// ─── Concurrency guard ─────────────────────────────────
	// mu serializes Job state transitions inside the scheduler.
	// Exported via UnsafeLock only for tests that need to construct a
	// job in a specific state. Normal consumers use Scheduler methods
	// which take the lock for them.
	mu sync.Mutex
}

// NewJob builds a Job with sensible defaults. The Validate pass runs
// inside Scheduler.Enqueue; callers don't need to invoke it directly.
func NewJob(name string, owner Owner, sched Schedule, action Action) *Job {
	now := time.Now()
	return &Job{
		Name:      strings.TrimSpace(name),
		Owner:     owner,
		Schedule:  sched,
		Action:    action,
		Status:    StatusPending,
		CreatedAt: now,
		UpdatedAt: now,
		Version:   SchemaVersion,
	}
}

// Clone returns a deep-ish copy suitable for returning to callers via
// `/jobs show` / IPC without exposing the internal mutex. Maps and
// slices are duplicated so mutations on the copy don't leak into the
// scheduler's working set.
func (j *Job) Clone() *Job {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.cloneLocked()
}

func (j *Job) cloneLocked() *Job {
	// Build the clone field-by-field to avoid copying j.mu (sync.Mutex
	// forbids copy-after-use — `go vet` would flag a struct assignment
	// even though the caller is holding the lock).
	dup := &Job{
		ID:                 j.ID,
		Name:               j.Name,
		Owner:              j.Owner,
		Version:            j.Version,
		Schedule:           j.Schedule,
		Action:             j.Action,
		ParentID:           j.ParentID,
		Budget:             j.Budget,
		TTL:                j.TTL,
		Status:             j.Status,
		NextFireAt:         j.NextFireAt,
		CreatedAt:          j.CreatedAt,
		UpdatedAt:          j.UpdatedAt,
		FinishedAt:         j.FinishedAt,
		PauseReason:        j.PauseReason,
		CancelReason:       j.CancelReason,
		HistoryLimit:       j.HistoryLimit,
		Attempts:           j.Attempts,
		DangerousConfirmed: j.DangerousConfirmed,
		Description:        j.Description,
	}
	if len(j.Tags) > 0 {
		dup.Tags = make(map[string]string, len(j.Tags))
		for k, v := range j.Tags {
			dup.Tags[k] = v
		}
	}
	if len(j.DependsOn) > 0 {
		dup.DependsOn = append([]JobID(nil), j.DependsOn...)
	}
	if len(j.Triggers) > 0 {
		dup.Triggers = append([]JobID(nil), j.Triggers...)
	}
	if len(j.Transitions) > 0 {
		dup.Transitions = append([]TransitionReason(nil), j.Transitions...)
	}
	if len(j.History) > 0 {
		dup.History = append([]ExecutionResult(nil), j.History...)
	}
	if j.LastResult != nil {
		lr := *j.LastResult
		dup.LastResult = &lr
	}
	if j.Wait != nil {
		w := *j.Wait
		dup.Wait = &w
	}
	return dup
}

// Validate runs admission-time checks. Called from Scheduler.Enqueue.
func (j *Job) Validate() error {
	if strings.TrimSpace(j.Name) == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidJob)
	}
	if j.Owner.Kind == "" {
		return fmt.Errorf("%w: owner.kind is required", ErrInvalidJob)
	}
	if err := j.Schedule.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSchedule, err)
	}
	if err := j.Action.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidAction, err)
	}
	if j.Wait != nil {
		if err := j.Wait.Validate(); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidCondition, err)
		}
	}
	if j.Budget.BackoffMult > 0 && j.Budget.BackoffMult < 1 {
		return fmt.Errorf("%w: backoff_mult must be >= 1", ErrInvalidJob)
	}
	if j.Budget.BackoffJitter < 0 || j.Budget.BackoffJitter > 0.5 {
		return fmt.Errorf("%w: backoff_jitter must be in [0, 0.5]", ErrInvalidJob)
	}
	return nil
}

// Lock and Unlock expose the Job mutex for Scheduler methods living in
// the same package. Not part of the public Go API because external
// callers interact through Scheduler.
func (j *Job) lock()   { j.mu.Lock() }
func (j *Job) unlock() { j.mu.Unlock() }

// JobSummary is the compact view returned by List and the status line.
// Stripping history and transitions keeps the JSON small for IPC.
type JobSummary struct {
	ID          JobID     `json:"id"`
	Name        string    `json:"name"`
	Owner       Owner     `json:"owner"`
	Status      JobStatus `json:"status"`
	Type        string    `json:"type"`
	NextFireAt  time.Time `json:"next_fire_at,omitempty"`
	LastOutcome Outcome   `json:"last_outcome,omitempty"`
	Attempts    int       `json:"attempts,omitempty"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Tags        []string  `json:"tags,omitempty"`
	// DangerousConfirmed mirrors Job.DangerousConfirmed so action
	// executors (notably action/shell.go) can pass it through to
	// CLIBridge.RunShell at fire time. Without this, a job that was
	// admitted to the queue with --i-know / i_know:true still failed
	// its fire-time policy re-check because the bridge had no way to
	// see the per-job authorization.
	DangerousConfirmed bool `json:"dangerous_confirmed,omitempty"`
}

// Summary projects a Job to a JobSummary.
func (j *Job) Summary() JobSummary {
	j.mu.Lock()
	defer j.mu.Unlock()
	var lastOut Outcome
	if j.LastResult != nil {
		lastOut = j.LastResult.Outcome
	}
	kind := string(j.Schedule.Kind)
	if j.Wait != nil {
		kind += "+wait"
	}
	tags := make([]string, 0, len(j.Tags))
	for k, v := range j.Tags {
		if v == "" {
			tags = append(tags, k)
		} else {
			tags = append(tags, k+"="+v)
		}
	}
	return JobSummary{
		ID:                 j.ID,
		Name:               j.Name,
		Owner:              j.Owner,
		Status:             j.Status,
		Type:               kind,
		NextFireAt:         j.NextFireAt,
		LastOutcome:        lastOut,
		Attempts:           j.Attempts,
		Description:        j.Description,
		CreatedAt:          j.CreatedAt,
		UpdatedAt:          j.UpdatedAt,
		Tags:               tags,
		DangerousConfirmed: j.DangerousConfirmed,
	}
}

// IsExpired returns true when the job's TTL has elapsed since it
// reached a terminal state. Scheduler.gc prunes expired records.
func (j *Job) IsExpired(now time.Time) bool {
	if j.TTL <= 0 || !j.Status.IsTerminal() || j.FinishedAt.IsZero() {
		return false
	}
	return now.Sub(j.FinishedAt) >= j.TTL
}
