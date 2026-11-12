/*
 * ChatCLI - Scheduler: agent tool adapter.
 *
 * The ReAct loop gains five tools that map directly onto the Scheduler
 * API:
 *
 *   schedule_job(spec)   — create a job; returns the ID.
 *   wait_until(spec)     — sync wait (agent yields); returns outcome.
 *                          With async=true, returns immediately after
 *                          creating the job (caller receives job.fired
 *                          event in next turn via AppendHistory).
 *   query_job(id)        — return a Job clone.
 *   list_jobs(filter)    — return summaries.
 *   cancel_job(id)       — cancel if authorized.
 *
 * The adapter is stateless: every method gets the owner context from
 * the caller (agent mode passes its agent name, the ChatCLI session
 * passes user). Authorization is enforced inside Scheduler itself.
 *
 * The adapter returns JSON strings suitable for direct insertion into
 * the ReAct loop's tool_result block. Callers just pass (input_json,
 * agent_owner) and get (result_json, error).
 */
package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ToolInput is the JSON shape the ReAct loop passes in.
type ToolInput struct {
	// schedule_job / wait_until inputs share most fields.
	Name        string            `json:"name,omitempty"`
	Description string            `json:"description,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`

	// Schedule.
	When     string    `json:"when,omitempty"`     // DSL string
	Schedule *Schedule `json:"schedule,omitempty"` // explicit
	// Action.
	Do     string  `json:"do,omitempty"`
	Action *Action `json:"action,omitempty"`
	// Wait.
	Until string    `json:"until,omitempty"`
	Wait  *WaitSpec `json:"wait,omitempty"`
	// Budget overrides.
	Timeout      string `json:"timeout,omitempty"`
	PollInterval string `json:"poll,omitempty"`
	MaxPolls     int    `json:"max_polls,omitempty"`
	WaitTimeout  string `json:"wait_timeout,omitempty"`
	MaxRetries   int    `json:"max_retries,omitempty"`
	OnTimeout    string `json:"on_timeout,omitempty"`
	// DAG.
	DependsOn []string `json:"depends_on,omitempty"`
	Triggers  []string `json:"triggers,omitempty"`
	// TTL.
	TTL string `json:"ttl,omitempty"`
	// async for wait_until.
	Async bool `json:"async,omitempty"`
	// MaxBlock caps how long a synchronous wait_until holds its caller.
	// When it elapses the job keeps running and the caller is told how to
	// pick the result up. Empty means the scheduler's default wait budget.
	MaxBlock string `json:"max_block,omitempty"`
	// Notify asks the terminal to announce the job's end (each cycle, for
	// a recurring job). Stored as the "notify" tag so it survives restarts
	// and reaches a daemon unchanged. Nil leaves the tags as given.
	Notify *bool `json:"notify,omitempty"`
	// OlderThan narrows clear_jobs to jobs finished at least this long ago.
	OlderThan string `json:"older_than,omitempty"`

	// IKnow pre-authorizes shell commands that would normally hit
	// ShellPolicyAsk at enqueue time. When true, the resulting Job
	// has DangerousConfirmed=true and passes preflight even on
	// Ask classifications. Denylist rules still bloquean — IKnow
	// cannot override Deny. Both the user (`--i-know` flag) and
	// agents (`i_know` field in the @scheduler tool call) may set
	// this; the authorization decision already happened at a higher
	// layer (the user ran /agent to spawn the agent, or the user
	// ran /schedule directly).
	IKnow bool `json:"i_know,omitempty"`

	// Skills are persona skill NAMES re-resolved at fire time and injected
	// into headless agent_task runs, so a scheduled card fires with the
	// same knowledge the creating run had. Names, not content: the daemon
	// process resolves them against its own skills directory (skill
	// structs do not serialize their content).
	Skills []string `json:"skills,omitempty"`

	// query/list/cancel inputs.
	ID     string     `json:"id,omitempty"`
	Filter ListFilter `json:"filter,omitempty"`
	Reason string     `json:"reason,omitempty"`
}

// ToolOutput is the normalized JSON shape returned to the ReAct loop.
type ToolOutput struct {
	OK      bool         `json:"ok"`
	JobID   JobID        `json:"job_id,omitempty"`
	Status  JobStatus    `json:"status,omitempty"`
	Summary *JobSummary  `json:"summary,omitempty"`
	Job     *Job         `json:"job,omitempty"`
	Jobs    []JobSummary `json:"jobs,omitempty"`
	Outcome Outcome      `json:"outcome,omitempty"`
	Details string       `json:"details,omitempty"`
	Error   string       `json:"error,omitempty"`
	// Removed lists the jobs clear_jobs dropped from history.
	Removed []JobID `json:"removed,omitempty"`
}

// ToolAdapter exposes the scheduler methods as JSON-in/JSON-out
// callable tools. Used by the ReAct loop and (via IPC) by the daemon.
type ToolAdapter struct {
	s *Scheduler
	// progress, when set, receives a heartbeat during synchronous WaitUntil
	// so long waits are visible instead of minutes of dead silence. Held
	// behind a pointer so the exported struct stays comparable (a bare func
	// field would be a breaking API change).
	progress *func(id JobID, status JobStatus, elapsed, remaining time.Duration)
}

// NewToolAdapter builds the adapter.
func NewToolAdapter(s *Scheduler) *ToolAdapter { return &ToolAdapter{s: s} }

// WithWaitProgress installs the synchronous-wait heartbeat callback.
func (t *ToolAdapter) WithWaitProgress(fn func(id JobID, status JobStatus, elapsed, remaining time.Duration)) *ToolAdapter {
	t.progress = &fn
	return t
}

// waitProgressInterval paces the WaitUntil heartbeat.
const waitProgressInterval = 30 * time.Second

// ScheduleJob implements the schedule_job tool.
func (t *ToolAdapter) ScheduleJob(ctx context.Context, owner Owner, rawIn string) (string, error) {
	var in ToolInput
	if err := json.Unmarshal([]byte(rawIn), &in); err != nil {
		return jsonError(err)
	}
	job, err := buildJobFromInput(&in, owner)
	if err != nil {
		return jsonError(err)
	}
	created, err := t.s.Enqueue(ctx, job)
	if err != nil {
		return jsonError(err)
	}
	out := ToolOutput{OK: true, JobID: created.ID, Status: created.Status}
	sum := created.Summary()
	out.Summary = &sum
	return jsonOK(out)
}

// WaitUntil implements the wait_until tool.
func (t *ToolAdapter) WaitUntil(ctx context.Context, owner Owner, rawIn string) (string, error) {
	var in ToolInput
	if err := json.Unmarshal([]byte(rawIn), &in); err != nil {
		return jsonError(err)
	}
	if in.Until == "" && in.Wait == nil {
		return jsonError(fmt.Errorf("wait_until: until or wait is required"))
	}
	// Build a waiter job.
	if in.Name == "" {
		in.Name = fmt.Sprintf("wait-%d", time.Now().UnixNano())
	}
	if in.When == "" && in.Schedule == nil {
		in.When = "when-ready"
	}
	if in.Do == "" && in.Action == nil {
		// Default action: noop. The wait itself is the point.
		in.Do = "noop"
	}
	job, err := buildJobFromInput(&in, owner)
	if err != nil {
		return jsonError(err)
	}
	created, err := t.s.Enqueue(ctx, job)
	if err != nil {
		return jsonError(err)
	}
	if in.Async {
		sum := created.Summary()
		return jsonOK(ToolOutput{OK: true, JobID: created.ID, Status: created.Status, Summary: &sum})
	}
	// Synchronous wait: poll the job's status.
	timeout := 35 * time.Minute
	if t.s.cfg.DefaultWaitTimeout > 0 {
		timeout = t.s.cfg.DefaultWaitTimeout + time.Minute
	}
	if strings.TrimSpace(in.MaxBlock) != "" {
		d, derr := ParseLooseDuration(in.MaxBlock)
		if derr != nil {
			return jsonError(fmt.Errorf("max_block: %w", derr))
		}
		if d > 0 && d < timeout {
			timeout = d
		}
	}
	ctx2, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	start := time.Now()
	nextBeat := start.Add(waitProgressInterval)
	for {
		select {
		case <-ctx2.Done():
			// Caller cancelled (Ctrl+C, IDE Stop): report the plain cause.
			if ctx.Err() != nil {
				return jsonError(ctx.Err())
			}
			// Our own wait budget elapsed. The job was NOT cancelled — it
			// keeps running in the scheduler — so tell the model exactly how
			// to pick it up instead of a bare "context deadline exceeded".
			status := created.Status
			if j, qerr := t.s.Query(created.ID); qerr == nil {
				status = j.Status
			}
			return jsonError(waitGaveUpError(timeout, created.ID, status))
		case <-ticker.C:
			j, err := t.s.Query(created.ID)
			if err != nil {
				return jsonError(err)
			}
			if j.Status.IsTerminal() {
				out := ToolOutput{OK: j.Status == StatusCompleted, JobID: j.ID, Status: j.Status, Job: j}
				if j.LastResult != nil {
					out.Outcome = j.LastResult.Outcome
					out.Details = j.LastResult.Output
				}
				return jsonOK(out)
			}
			// Heartbeat: a synchronous wait can legitimately run for many
			// minutes — surface life signs instead of dead silence.
			if t.progress != nil && time.Now().After(nextBeat) {
				elapsed := time.Since(start).Round(time.Second)
				remaining := (timeout - time.Since(start)).Round(time.Second)
				(*t.progress)(j.ID, j.Status, elapsed, remaining)
				nextBeat = time.Now().Add(waitProgressInterval)
			}
		}
	}
}

// QueryJob implements the query_job tool.
func (t *ToolAdapter) QueryJob(_ context.Context, _ Owner, rawIn string) (string, error) {
	var in ToolInput
	if err := json.Unmarshal([]byte(rawIn), &in); err != nil {
		return jsonError(err)
	}
	if in.ID == "" {
		return jsonError(fmt.Errorf("query_job: id is required"))
	}
	ref, err := t.s.ResolveRef(in.ID)
	if err != nil {
		return jsonError(err)
	}
	in.ID = string(ref)
	j, err := t.s.Query(JobID(in.ID))
	if err != nil {
		return jsonError(err)
	}
	return jsonOK(ToolOutput{OK: true, Job: j, Status: j.Status})
}

// ListJobs implements the list_jobs tool.
func (t *ToolAdapter) ListJobs(_ context.Context, owner Owner, rawIn string) (string, error) {
	var in ToolInput
	if strings.TrimSpace(rawIn) != "" {
		if err := json.Unmarshal([]byte(rawIn), &in); err != nil {
			return jsonError(err)
		}
	}
	filter := in.Filter
	// Scope: agents see their own by default unless they pass owner=all.
	if filter.Owner == nil && owner.Kind == OwnerAgent {
		o := owner
		filter.Owner = &o
	}
	list := t.s.List(filter)
	return jsonOK(ToolOutput{OK: true, Jobs: list})
}

// CancelJob implements the cancel_job tool.
func (t *ToolAdapter) CancelJob(_ context.Context, owner Owner, rawIn string) (string, error) {
	var in ToolInput
	if err := json.Unmarshal([]byte(rawIn), &in); err != nil {
		return jsonError(err)
	}
	if in.ID == "" {
		return jsonError(fmt.Errorf("cancel_job: id is required"))
	}
	ref, err := t.s.ResolveRef(in.ID)
	if err != nil {
		return jsonError(err)
	}
	in.ID = string(ref)
	if err := t.s.Cancel(JobID(in.ID), firstNonEmpty(in.Reason, "agent-cancelled"), owner); err != nil {
		return jsonError(err)
	}
	return jsonOK(ToolOutput{OK: true, JobID: JobID(in.ID), Status: StatusCancelled})
}

// PauseJob implements the pause_job tool: a pending, blocked or waiting
// job leaves the queue until resumed (a running one must be cancelled).
func (t *ToolAdapter) PauseJob(_ context.Context, owner Owner, rawIn string) (string, error) {
	var in ToolInput
	if err := json.Unmarshal([]byte(rawIn), &in); err != nil {
		return jsonError(err)
	}
	if in.ID == "" {
		return jsonError(fmt.Errorf("pause_job: id is required"))
	}
	ref, err := t.s.ResolveRef(in.ID)
	if err != nil {
		return jsonError(err)
	}
	in.ID = string(ref)
	if err := t.s.Pause(JobID(in.ID), firstNonEmpty(in.Reason, "agent-paused"), owner); err != nil {
		return jsonError(err)
	}
	return jsonOK(ToolOutput{OK: true, JobID: JobID(in.ID), Status: StatusPaused})
}

// ResumeJob implements the resume_job tool: a paused job goes back to
// pending and re-enters the queue.
func (t *ToolAdapter) ResumeJob(_ context.Context, owner Owner, rawIn string) (string, error) {
	var in ToolInput
	if err := json.Unmarshal([]byte(rawIn), &in); err != nil {
		return jsonError(err)
	}
	if in.ID == "" {
		return jsonError(fmt.Errorf("resume_job: id is required"))
	}
	ref, err := t.s.ResolveRef(in.ID)
	if err != nil {
		return jsonError(err)
	}
	in.ID = string(ref)
	if err := t.s.Resume(JobID(in.ID), owner); err != nil {
		return jsonError(err)
	}
	return jsonOK(ToolOutput{OK: true, JobID: JobID(in.ID), Status: StatusPending})
}

// ClearJobs implements the clear_jobs tool: drop finished jobs from the
// history. Running, waiting and queued jobs are never touched. An agent or
// worker clears only what it owns; the filter's owner is ignored for them.
func (t *ToolAdapter) ClearJobs(_ context.Context, owner Owner, rawIn string) (string, error) {
	var in ToolInput
	if strings.TrimSpace(rawIn) != "" {
		if err := json.Unmarshal([]byte(rawIn), &in); err != nil {
			return jsonError(err)
		}
	}
	filter, err := PruneFilterFromInput(in, owner)
	if err != nil {
		return jsonError(err)
	}
	removed, err := t.s.Prune(filter)
	if err != nil {
		return jsonError(err)
	}
	if removed == nil {
		removed = []JobID{}
	}
	return jsonOK(ToolOutput{OK: true, Removed: removed, Details: fmt.Sprintf("%d finished job(s) removed from history", len(removed))})
}

// PruneFilterFromInput builds the clear_jobs filter from a tool input,
// scoping agents and workers to their own jobs.
func PruneFilterFromInput(in ToolInput, owner Owner) (PruneFilter, error) {
	filter := PruneFilter{
		Statuses:   in.Filter.Statuses,
		Owner:      in.Filter.Owner,
		NameSubstr: in.Filter.NameSubstr,
	}
	for _, st := range filter.Statuses {
		if !st.IsTerminal() {
			return PruneFilter{}, fmt.Errorf("clear_jobs: status %q is not a finished state (use completed, failed, cancelled, timed_out or skipped); cancel a live job instead", st)
		}
	}
	if owner.Kind == OwnerAgent || owner.Kind == OwnerWorker {
		o := owner
		filter.Owner = &o
	}
	if strings.TrimSpace(in.OlderThan) != "" {
		d, err := ParseLooseDuration(in.OlderThan)
		if err != nil {
			return PruneFilter{}, fmt.Errorf("older_than: %w", err)
		}
		filter.OlderThan = d
	}
	return filter, nil
}

// ─── helpers ──────────────────────────────────────────────────

func buildJobFromInput(in *ToolInput, owner Owner) (*Job, error) {
	// Schedule.
	var sched Schedule
	switch {
	case in.Schedule != nil:
		sched = *in.Schedule
	case in.When != "":
		parsed, err := ParseScheduleDSL(in.When)
		if err != nil {
			return nil, fmt.Errorf("schedule parse: %w", err)
		}
		sched = parsed
	default:
		// Default: immediate one-shot.
		sched = Schedule{Kind: ScheduleRelative, Relative: 0}
	}

	// Action.
	var act Action
	switch {
	case in.Action != nil:
		act = *in.Action
	case in.Do != "":
		parsed, err := ParseActionDSL(in.Do)
		if err != nil {
			return nil, fmt.Errorf("action parse: %w", err)
		}
		act = parsed
	default:
		return nil, fmt.Errorf("action: do or action is required")
	}

	// Wait.
	var wait *WaitSpec
	if in.Wait != nil {
		wait = in.Wait
	} else if in.Until != "" {
		cond, err := ParseConditionDSL(in.Until)
		if err != nil {
			return nil, fmt.Errorf("wait condition parse: %w", err)
		}
		wait = &WaitSpec{Condition: cond}
	}
	if in.OnTimeout != "" {
		behavior, err := parseTimeoutBehavior(in.OnTimeout)
		if err != nil {
			return nil, err
		}
		if wait != nil {
			wait.OnTimeout = behavior
		}
	}

	// Name.
	if in.Name == "" {
		in.Name = fmt.Sprintf("job-%d", time.Now().UnixNano())
	}

	// Skills ride the action payload (JSON-safe by construction) so the
	// executor — possibly a daemon process with no access to the creating
	// session — can re-resolve and inject them at fire time.
	if len(in.Skills) > 0 && act.Type == ActionAgentTask {
		if act.Payload == nil {
			act.Payload = make(map[string]any)
		}
		act.Payload["skills"] = in.Skills
	}

	job := NewJob(in.Name, owner, sched, act)
	job.Wait = wait
	job.Description = in.Description
	job.Tags = in.Tags
	applyNotify(in, job)
	// --i-know / i_know pre-authorizes ShellPolicyAsk commands at
	// preflight. Denylist is NOT overridable here.
	job.DangerousConfirmed = in.IKnow
	if err := applyBudget(in, job); err != nil {
		return nil, err
	}
	if len(in.DependsOn) > 0 {
		ids := make([]JobID, len(in.DependsOn))
		for i, s := range in.DependsOn {
			ids[i] = JobID(s)
		}
		job.DependsOn = ids
	}
	if len(in.Triggers) > 0 {
		ids := make([]JobID, len(in.Triggers))
		for i, s := range in.Triggers {
			ids[i] = JobID(s)
		}
		job.Triggers = ids
	}
	return job, nil
}

// applyBudget copies the input's timeouts and limits onto the job. A
// duration that cannot be read is an error, never a silent fall back to
// the default: "--timeout 10x" used to leave the wait on 30 minutes.
func applyBudget(in *ToolInput, job *Job) error {
	for _, f := range []struct {
		name, raw string
		dst       *time.Duration
	}{
		{"ttl", in.TTL, &job.TTL},
		{"timeout", in.Timeout, &job.Budget.ActionTimeout},
		{"poll", in.PollInterval, &job.Budget.PollInterval},
		{"wait_timeout", in.WaitTimeout, &job.Budget.WaitTimeout},
	} {
		if strings.TrimSpace(f.raw) == "" {
			continue
		}
		d, err := ParseLooseDuration(f.raw)
		if err != nil {
			return fmt.Errorf("%s: %w", f.name, err)
		}
		if d > 0 { // zero keeps the configured default
			*f.dst = d
		}
	}
	if in.MaxPolls < 0 || in.MaxRetries < 0 {
		return fmt.Errorf("max_polls and max_retries cannot be negative")
	}
	if in.MaxPolls > 0 {
		job.Budget.MaxPolls = in.MaxPolls
	}
	if in.MaxRetries > 0 {
		job.Budget.MaxRetries = in.MaxRetries
	}
	return nil
}

// applyNotify stores the input's notify choice as the job's "notify" tag.
func applyNotify(in *ToolInput, job *Job) {
	if in.Notify == nil {
		return
	}
	if job.Tags == nil {
		job.Tags = map[string]string{}
	}
	job.Tags["notify"] = "off"
	if *in.Notify {
		job.Tags["notify"] = "on"
	}
}

// parseTimeoutBehavior reads on_timeout tolerantly ("fire-anyway", "run",
// "fail") and rejects what it does not know: an unknown value used to be
// stored as is and then treated as "fail" at fire time.
func parseTimeoutBehavior(raw string) (TimeoutBehavior, error) {
	switch strings.ToLower(strings.NewReplacer("-", "_", " ", "_").Replace(strings.TrimSpace(raw))) {
	case "fail", "failed", "error", "abort":
		return TimeoutFail, nil
	case "fire_anyway", "fireanyway", "fire", "run", "run_anyway", "continue", "proceed":
		return TimeoutFireAnyway, nil
	case "fallback":
		return TimeoutFallback, nil
	}
	return "", fmt.Errorf("on_timeout: unknown value %q (use fail, fire_anyway or fallback)", raw)
}

func jsonOK(v ToolOutput) (string, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// waitGaveUpError builds the actionable wait_until timeout message: the job
// keeps running, so the model must learn how to pick the result up later —
// a bare "context deadline exceeded" taught it nothing.
func waitGaveUpError(timeout time.Duration, id JobID, status JobStatus) error {
	return fmt.Errorf(
		"wait_until: gave up waiting after %s; job %s is still %s and KEEPS RUNNING in background. Check it later with query_job {\"id\":%q}, or re-issue the wait with \"async\":true and react to the job event in a future turn",
		timeout.Round(time.Second), id, status, id)
}

func jsonError(err error) (string, error) {
	out := ToolOutput{OK: false, Error: err.Error()}
	b, _ := json.MarshalIndent(out, "", "  ")
	return string(b), nil
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}
