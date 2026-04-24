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
}

// ToolAdapter exposes the scheduler methods as JSON-in/JSON-out
// callable tools. Used by the ReAct loop and (via IPC) by the daemon.
type ToolAdapter struct {
	s *Scheduler
}

// NewToolAdapter builds the adapter.
func NewToolAdapter(s *Scheduler) *ToolAdapter { return &ToolAdapter{s: s} }

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
	ctx2, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx2.Done():
			return jsonError(ctx2.Err())
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
	if err := t.s.Cancel(JobID(in.ID), firstNonEmpty(in.Reason, "agent-cancelled"), owner); err != nil {
		return jsonError(err)
	}
	return jsonOK(ToolOutput{OK: true, JobID: JobID(in.ID), Status: StatusCancelled})
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
	if wait != nil && in.OnTimeout != "" {
		wait.OnTimeout = TimeoutBehavior(in.OnTimeout)
	}

	// Name.
	if in.Name == "" {
		in.Name = fmt.Sprintf("job-%d", time.Now().UnixNano())
	}

	job := NewJob(in.Name, owner, sched, act)
	job.Wait = wait
	job.Description = in.Description
	job.Tags = in.Tags
	// --i-know / i_know pre-authorizes ShellPolicyAsk commands at
	// preflight. Denylist is NOT overridable here.
	job.DangerousConfirmed = in.IKnow
	if in.TTL != "" {
		if d, err := time.ParseDuration(in.TTL); err == nil {
			job.TTL = d
		}
	}
	if in.Timeout != "" {
		if d, err := time.ParseDuration(in.Timeout); err == nil {
			job.Budget.ActionTimeout = d
		}
	}
	if in.PollInterval != "" {
		if d, err := time.ParseDuration(in.PollInterval); err == nil {
			job.Budget.PollInterval = d
		}
	}
	if in.WaitTimeout != "" {
		if d, err := time.ParseDuration(in.WaitTimeout); err == nil {
			job.Budget.WaitTimeout = d
		}
	}
	if in.MaxPolls > 0 {
		job.Budget.MaxPolls = in.MaxPolls
	}
	if in.MaxRetries > 0 {
		job.Budget.MaxRetries = in.MaxRetries
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

func jsonOK(v ToolOutput) (string, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
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
