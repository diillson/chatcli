/*
 * ChatCLI - Scheduler: sentinel errors.
 *
 * Every surface-facing error the scheduler can return is declared here
 * so callers (CLI handlers, agent tool adapters, daemon IPC) can match
 * with errors.Is. Internal-only errors that never cross a package
 * boundary stay inline where they occur.
 */
package scheduler

import "errors"

// Lifecycle.
var (
	// ErrSchedulerClosed is returned from any public method after the
	// scheduler has been stopped. Callers should treat this as terminal
	// for the current instance — a new Scheduler must be constructed to
	// accept work again.
	ErrSchedulerClosed = errors.New("scheduler: closed")

	// ErrSchedulerDraining is returned from Enqueue after Stop has been
	// called but before all in-flight jobs have finished. New work is
	// rejected; already-queued jobs continue to run.
	ErrSchedulerDraining = errors.New("scheduler: draining")

	// ErrNotStarted is returned when a method that requires a running
	// loop is called before Start.
	ErrNotStarted = errors.New("scheduler: not started")
)

// Validation — raised during parse / admission.
var (
	// ErrInvalidJob wraps a specific validation failure from Job.Validate.
	ErrInvalidJob = errors.New("scheduler: invalid job")

	// ErrInvalidSchedule indicates a Schedule whose fields are
	// inconsistent with its Kind (e.g. absolute schedule with zero
	// time, cron with empty expression).
	ErrInvalidSchedule = errors.New("scheduler: invalid schedule")

	// ErrInvalidCondition is returned when a Condition.Type is unknown
	// to the registry, or when the Spec is missing required fields.
	ErrInvalidCondition = errors.New("scheduler: invalid condition")

	// ErrInvalidAction mirrors ErrInvalidCondition for actions.
	ErrInvalidAction = errors.New("scheduler: invalid action")

	// ErrDuplicateName is returned when Enqueue sees a job name already
	// held by a non-terminal job. Job IDs are globally unique (derived
	// from name + owner + nonce) — but users expect human-readable
	// names to be unique within their scope, so we enforce it.
	ErrDuplicateName = errors.New("scheduler: duplicate name")

	// ErrDAGCycle is returned when the caller passes DependsOn edges
	// that form a cycle. Scheduler refuses to admit such jobs.
	ErrDAGCycle = errors.New("scheduler: dag cycle")
)

// Runtime — raised while executing.
var (
	// ErrJobNotFound is returned from Query / Cancel / Pause when the
	// given ID has no record in the active set.
	ErrJobNotFound = errors.New("scheduler: job not found")

	// ErrJobTerminal is returned when a mutating operation is attempted
	// on a job that has already reached a terminal state (completed,
	// failed, cancelled).
	ErrJobTerminal = errors.New("scheduler: job is terminal")

	// ErrWaitTimeout is returned by evaluators that exhausted their
	// MaxPolls or WaitTimeout without the condition ever being
	// satisfied.
	ErrWaitTimeout = errors.New("scheduler: wait condition timed out")

	// ErrBreakerOpen is returned when an evaluator or action is skipped
	// because its circuit breaker is open. The scheduler still marks
	// the job as failed for metric purposes; the message surfaces why.
	ErrBreakerOpen = errors.New("scheduler: circuit breaker open")

	// ErrRateLimited is returned from Enqueue when the per-owner or
	// global rate limit would be exceeded. Callers may retry after
	// the Retry-After hint surfaced on the underlying limiter error.
	ErrRateLimited = errors.New("scheduler: rate limited")

	// ErrQueueFull is returned when Enqueue cannot admit more jobs
	// and the overflow policy is Block (with timeout elapsed) or when
	// the WAL has already reached MaxJobs.
	ErrQueueFull = errors.New("scheduler: queue full")
)

// Authorization.
var (
	// ErrNotAuthorized is returned when a caller (typically an agent)
	// tries to mutate a job owned by a different principal.
	ErrNotAuthorized = errors.New("scheduler: not authorized")

	// ErrActionDisallowed is returned when a requested Action type is
	// not in the operator-configured allowlist.
	ErrActionDisallowed = errors.New("scheduler: action type not in allowlist")

	// ErrDangerousShell is returned when a shell action's command
	// matches a pattern flagged by the shell safety filter and the
	// job was not submitted with the explicit --i-know flag.
	ErrDangerousShell = errors.New("scheduler: dangerous shell command requires explicit confirmation")

	// ErrShellPolicyDeny is returned when a shell command (in an
	// action or wait condition) is on the CoderMode denylist.
	// Unlike ErrDangerousShell, this cannot be overridden by --i-know
	// — denylist beats user confirmation. The job is rejected at
	// enqueue time and never admitted to the WAL.
	ErrShellPolicyDeny = errors.New("scheduler: shell command denied by policy")

	// ErrShellPolicyAsk is returned when a shell command would
	// normally require interactive approval under CoderMode. The
	// scheduler has no interactive channel at fire time, so the
	// command is rejected unless the job was created with --i-know
	// (which sets Job.DangerousConfirmed=true). When re-checked at
	// fire time (policy may have changed since enqueue), the same
	// error is returned if the classification drifted to Ask.
	ErrShellPolicyAsk = errors.New("scheduler: shell command requires approval; use --i-know to pre-authorize or add to /config security allowlist")
)

// Persistence.
var (
	// ErrWALCorrupted is returned when a WAL record fails CRC validation
	// and is not recoverable. The record is quarantined to the DLQ.
	ErrWALCorrupted = errors.New("scheduler: wal record corrupted")
)

// Daemon.
var (
	// ErrNoDaemon is returned by the IPC client when no daemon is
	// reachable on the configured socket path. Callers may fall back
	// to in-process scheduling or prompt the user to start the daemon.
	ErrNoDaemon = errors.New("scheduler: no daemon running")

	// ErrDaemonRunning is returned by `chatcli daemon start` when a
	// daemon is already bound to the socket — starting a second daemon
	// would split the WAL between two writers.
	ErrDaemonRunning = errors.New("scheduler: daemon already running")

	// ErrIPCProtocol covers any malformed / unrecognized frame on the
	// daemon socket.
	ErrIPCProtocol = errors.New("scheduler: ipc protocol error")
)
