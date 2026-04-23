/*
 * ChatCLI - Scheduler: event payloads + publishing helpers.
 *
 * The scheduler uses three notification channels, each serving a
 * different audience:
 *
 *   1. Event bus (cli/bus) — live fan-out to UI subscribers (status
 *      line, Ctrl+J overlay). Non-durable; dropped on disconnect.
 *
 *   2. Hook manager (cli/hooks) — user-configured command/HTTP
 *      callbacks. Durable to the extent the hook command is.
 *
 *   3. Audit log — append-only JSONL for forensics. Always written.
 *
 * This file defines the payload shape shared by all three. Scheduler
 * helper methods (emit*) centralize the triple-fan-out so every state
 * change produces a consistent observability signal.
 */
package scheduler

import "time"

// EventType enumerates the scheduler-specific events. All values are
// strings so they round-trip cleanly through JSON (IPC, audit).
type EventType string

const (
	EventJobCreated       EventType = "job.created"
	EventJobScheduled     EventType = "job.scheduled"
	EventJobFired         EventType = "job.fired"
	EventJobWaitStarted   EventType = "job.wait_started"
	EventJobWaitTick      EventType = "job.wait_tick"
	EventJobWaitSatisfied EventType = "job.wait_satisfied"
	EventJobRunning       EventType = "job.running"
	EventJobCompleted     EventType = "job.completed"
	EventJobFailed        EventType = "job.failed"
	EventJobTimedOut      EventType = "job.timed_out"
	EventJobCancelled     EventType = "job.cancelled"
	EventJobSkipped       EventType = "job.skipped"
	EventJobRetryQueued   EventType = "job.retry_queued"
	EventJobPaused        EventType = "job.paused"
	EventJobResumed       EventType = "job.resumed"
	EventJobDependency    EventType = "job.dependency_resolved"
	EventBreakerOpened    EventType = "breaker.opened"
	EventBreakerHalfOpen  EventType = "breaker.half_open"
	EventBreakerClosed    EventType = "breaker.closed"
	EventDaemonStarted    EventType = "daemon.started"
	EventDaemonStopped    EventType = "daemon.stopped"
)

// Event is the payload fan-out. JSON-serialized for IPC + audit; bus
// subscribers receive the struct directly.
type Event struct {
	Type      EventType              `json:"type"`
	Timestamp time.Time              `json:"timestamp"`
	JobID     JobID                  `json:"job_id,omitempty"`
	Name      string                 `json:"name,omitempty"`
	Owner     Owner                  `json:"owner,omitempty"`
	Status    JobStatus              `json:"status,omitempty"`
	Outcome   Outcome                `json:"outcome,omitempty"`
	Message   string                 `json:"message,omitempty"`
	Data      map[string]any         `json:"data,omitempty"`
	Execution *ExecutionResult       `json:"execution,omitempty"`
}

// NewEvent stamps the timestamp and returns a fresh Event value.
func NewEvent(t EventType) Event {
	return Event{Type: t, Timestamp: time.Now()}
}

// WithJob enriches an event with job identity fields.
func (e Event) WithJob(j *Job) Event {
	j.lock()
	defer j.unlock()
	e.JobID = j.ID
	e.Name = j.Name
	e.Owner = j.Owner
	e.Status = j.Status
	return e
}

// WithMessage adds a human-readable message.
func (e Event) WithMessage(msg string) Event {
	e.Message = msg
	return e
}

// WithData attaches arbitrary k/v payload.
func (e Event) WithData(k string, v any) Event {
	if e.Data == nil {
		e.Data = make(map[string]any)
	}
	e.Data[k] = v
	return e
}

// WithExecution attaches an ExecutionResult (typically on job.completed
// or job.failed).
func (e Event) WithExecution(r ExecutionResult) Event {
	r2 := r
	e.Execution = &r2
	return e
}
