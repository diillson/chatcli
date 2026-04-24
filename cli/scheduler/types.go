/*
 * ChatCLI - Scheduler: core value types.
 *
 * Every type here is a plain value type. Behavior lives in separate
 * files (scheduler.go, queue.go, state_machine.go, action/, condition/).
 * Keeping types/behavior separated makes the schema trivial to JSON
 * encode for the WAL, the IPC protocol and the audit log.
 */
package scheduler

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ─── Identity ───────────────────────────────────────────────────

// JobID is the globally unique identifier. Derived from
// sha256(name|owner|created_nonce) truncated to 16 hex chars. See
// DeriveJobID in id.go. Stable across retries — re-Enqueue of the
// same (name, owner) produces the same JobID and is an idempotent
// no-op.
type JobID string

// String returns the ID as a plain string for logging / display.
func (j JobID) String() string { return string(j) }

// IsZero reports whether the ID is empty (used in validation paths).
func (j JobID) IsZero() bool { return strings.TrimSpace(string(j)) == "" }

// OwnerKind classifies who created a job.
type OwnerKind string

const (
	// OwnerUser — direct /schedule or /wait invocation by the human.
	OwnerUser OwnerKind = "user"
	// OwnerAgent — created inside the ReAct loop via a tool call.
	OwnerAgent OwnerKind = "agent"
	// OwnerWorker — created by a subagent (worker dispatcher).
	OwnerWorker OwnerKind = "worker"
	// OwnerSystem — scheduler itself (e.g. retry follow-ups).
	OwnerSystem OwnerKind = "system"
	// OwnerHook — triggered by a hook firing.
	OwnerHook OwnerKind = "hook"
)

// Owner identifies the principal responsible for a job. Used for
// authorization (cancel / pause) and filtering (`/jobs list --owner me`).
type Owner struct {
	Kind OwnerKind `json:"kind"`
	// ID is the owner-specific identifier (session id for user,
	// agent name for agent, worker call id for worker).
	ID string `json:"id"`
	// Tag is a free-form label for grouping in the UI. Optional.
	Tag string `json:"tag,omitempty"`
}

// String returns "kind:id" for compact log/display lines.
func (o Owner) String() string {
	if o.ID == "" {
		return string(o.Kind)
	}
	return string(o.Kind) + ":" + o.ID
}

// Equal compares two owners by Kind + ID only. Tag is ignored — two
// user-owned jobs from the same session are considered the same owner
// even if they were tagged differently.
func (o Owner) Equal(other Owner) bool {
	return o.Kind == other.Kind && o.ID == other.ID
}

// ─── Status ────────────────────────────────────────────────────

// JobStatus is the externally observable lifecycle state. Transitions
// are enforced by stateMachine.Transition; see state_machine.go.
type JobStatus string

const (
	// StatusPending — job is registered, waiting for its NextFireAt.
	StatusPending JobStatus = "pending"
	// StatusBlocked — job is waiting for one or more DependsOn jobs.
	StatusBlocked JobStatus = "blocked"
	// StatusWaiting — poll loop active, evaluating a wait condition.
	StatusWaiting JobStatus = "waiting"
	// StatusRunning — action is executing.
	StatusRunning JobStatus = "running"
	// StatusPaused — user asked to hold; no evaluation until Resume.
	StatusPaused JobStatus = "paused"
	// StatusCompleted — action succeeded.
	StatusCompleted JobStatus = "completed"
	// StatusFailed — action (or wait) returned an error.
	StatusFailed JobStatus = "failed"
	// StatusCancelled — user or owner asked to stop mid-flight.
	StatusCancelled JobStatus = "cancelled"
	// StatusTimedOut — wait condition never satisfied within WaitTimeout
	// and the timeout behavior was "fail".
	StatusTimedOut JobStatus = "timed_out"
	// StatusSkipped — missed-fire policy decided to drop the occurrence.
	StatusSkipped JobStatus = "skipped"
)

// IsTerminal reports whether no further transitions are possible.
func (s JobStatus) IsTerminal() bool {
	switch s {
	case StatusCompleted, StatusFailed, StatusCancelled, StatusTimedOut, StatusSkipped:
		return true
	}
	return false
}

// IsActive reports whether the job currently occupies a worker slot
// or is otherwise doing work. Used for queue depth metrics.
func (s JobStatus) IsActive() bool {
	switch s {
	case StatusWaiting, StatusRunning:
		return true
	}
	return false
}

// ─── Outcome (per-execution) ───────────────────────────────────

// Outcome classifies a single ExecutionResult. Distinct from JobStatus
// because a job may have many executions (cron, retries, interval) and
// each one has its own Outcome.
type Outcome string

const (
	OutcomeSuccess    Outcome = "success"
	OutcomeFailed     Outcome = "failed"
	OutcomeTimeout    Outcome = "timeout"
	OutcomeCancelled  Outcome = "cancelled"
	OutcomeBreakerOff Outcome = "breaker_open"
	OutcomeSkipped    Outcome = "skipped"
)

// ─── Execution results ─────────────────────────────────────────

// ExecutionResult captures one attempt — either a wait-condition poll
// result (from an on_condition schedule) or an action execution.
// Stored in Job.History as a ring buffer.
type ExecutionResult struct {
	AttemptNum int           `json:"attempt"`
	StartedAt  time.Time     `json:"started_at"`
	FinishedAt time.Time     `json:"finished_at"`
	Duration   time.Duration `json:"duration_ns"`
	Outcome    Outcome       `json:"outcome"`
	Output     string        `json:"output,omitempty"`
	// Error holds the stringified error message; we keep it as a
	// string (not an error value) because ExecutionResult travels
	// through JSON (WAL, IPC, audit log).
	Error string `json:"error,omitempty"`
	// Tokens / Cost surface LLM-action cost to /jobs show.
	Tokens int     `json:"tokens,omitempty"`
	Cost   float64 `json:"cost_usd,omitempty"`
	// ConditionSatisfied is true when a wait_until job's condition
	// flipped on this poll. Left empty for non-wait executions.
	ConditionSatisfied bool   `json:"condition_satisfied,omitempty"`
	ConditionDetails   string `json:"condition_details,omitempty"`
}

// Succeeded reports whether the execution ended in OutcomeSuccess.
// Helper for hook event payloads and audit queries.
func (r ExecutionResult) Succeeded() bool { return r.Outcome == OutcomeSuccess }

// ─── Budget ────────────────────────────────────────────────────

// Budget bundles every "how long / how many times" limit the scheduler
// enforces on a single job. Any zero field is interpreted as "use
// the scheduler default from Config".
type Budget struct {
	// ActionTimeout bounds a single execution of Action.
	ActionTimeout time.Duration `json:"action_timeout,omitempty"`

	// MaxRetries is the total number of retries after a failure
	// (so MaxRetries=3 means up to 4 executions total).
	MaxRetries int `json:"max_retries,omitempty"`

	// Backoff describes the retry delay ramp.
	BackoffInitial time.Duration `json:"backoff_initial,omitempty"`
	BackoffMax     time.Duration `json:"backoff_max,omitempty"`
	BackoffMult    float64       `json:"backoff_mult,omitempty"`
	BackoffJitter  float64       `json:"backoff_jitter,omitempty"` // 0..0.5

	// WaitTimeout caps the total time a wait-until loop may consume.
	WaitTimeout time.Duration `json:"wait_timeout,omitempty"`

	// PollInterval is the gap between wait-condition evaluations.
	PollInterval time.Duration `json:"poll_interval,omitempty"`

	// MaxPolls caps the number of wait evaluations. Zero = unlimited
	// (bounded only by WaitTimeout).
	MaxPolls int `json:"max_polls,omitempty"`
}

// Merge fills zero fields in b from other, returning the merged copy.
// Used to layer Job.Budget over Config defaults.
func (b Budget) Merge(other Budget) Budget {
	if b.ActionTimeout == 0 {
		b.ActionTimeout = other.ActionTimeout
	}
	if b.MaxRetries == 0 {
		b.MaxRetries = other.MaxRetries
	}
	if b.BackoffInitial == 0 {
		b.BackoffInitial = other.BackoffInitial
	}
	if b.BackoffMax == 0 {
		b.BackoffMax = other.BackoffMax
	}
	if b.BackoffMult == 0 {
		b.BackoffMult = other.BackoffMult
	}
	if b.BackoffJitter == 0 {
		b.BackoffJitter = other.BackoffJitter
	}
	if b.WaitTimeout == 0 {
		b.WaitTimeout = other.WaitTimeout
	}
	if b.PollInterval == 0 {
		b.PollInterval = other.PollInterval
	}
	if b.MaxPolls == 0 {
		b.MaxPolls = other.MaxPolls
	}
	return b
}

// ─── Timeout-on-wait behaviors ─────────────────────────────────

// TimeoutBehavior decides what happens when a wait-until loop runs
// out of time or poll budget.
type TimeoutBehavior string

const (
	// TimeoutFail — mark job StatusTimedOut. Default.
	TimeoutFail TimeoutBehavior = "fail"
	// TimeoutFireAnyway — run the Action even though the condition
	// never turned true. Useful for "cleanup regardless" semantics.
	TimeoutFireAnyway TimeoutBehavior = "fire_anyway"
	// TimeoutFallback — run WaitSpec.Fallback (a different Action) and
	// then fail. Used to notify when a deployment never came up.
	TimeoutFallback TimeoutBehavior = "fallback"
)

// ─── WaitSpec ──────────────────────────────────────────────────

// WaitSpec optionally gates an Action behind a polled condition.
// When Wait is non-nil on a Job, the scheduler enters StatusWaiting
// at NextFireAt and stays there, evaluating WaitSpec.Condition every
// PollInterval, until the condition is satisfied or the budget runs
// out.
type WaitSpec struct {
	Condition Condition       `json:"condition"`
	OnTimeout TimeoutBehavior `json:"on_timeout,omitempty"`
	// Fallback is consulted when OnTimeout=TimeoutFallback.
	Fallback *Action `json:"fallback,omitempty"`
}

// Validate returns a non-nil error when the WaitSpec is malformed.
// Called during Enqueue admission.
func (w *WaitSpec) Validate() error {
	if w == nil {
		return nil
	}
	if err := w.Condition.Validate(); err != nil {
		return fmt.Errorf("wait.condition: %w", err)
	}
	if w.OnTimeout == TimeoutFallback && (w.Fallback == nil || w.Fallback.Type == "") {
		return fmt.Errorf("wait.on_timeout=fallback requires wait.fallback to be set")
	}
	return nil
}

// ─── Condition ─────────────────────────────────────────────────

// Condition is the polymorphic descriptor the scheduler hands to a
// ConditionEvaluator at evaluation time. Type picks the evaluator;
// Spec carries its parameters; Children allows composites (all_of,
// any_of, not).
type Condition struct {
	Type     string         `json:"type"`
	Spec     map[string]any `json:"spec,omitempty"`
	Children []Condition    `json:"children,omitempty"`
	Negate   bool           `json:"negate,omitempty"`
}

// Validate checks that the Condition has a Type and that the Spec is
// usable. Type-specific validation happens in the evaluator itself
// (evaluator.ValidateSpec). This pass only catches structural errors.
func (c Condition) Validate() error {
	t := strings.TrimSpace(c.Type)
	if t == "" {
		return fmt.Errorf("condition type is required")
	}
	if t == "all_of" || t == "any_of" {
		if len(c.Children) == 0 {
			return fmt.Errorf("composite condition %q requires children", t)
		}
		for i, child := range c.Children {
			if err := child.Validate(); err != nil {
				return fmt.Errorf("children[%d]: %w", i, err)
			}
		}
	}
	return nil
}

// SpecInt reads an int field from Spec with a default.
func (c Condition) SpecInt(key string, def int) int {
	if v, ok := c.Spec[key]; ok {
		switch n := v.(type) {
		case int:
			return n
		case int64:
			return int(n)
		case float64:
			return int(n)
		}
	}
	return def
}

// SpecString reads a string field from Spec with a default.
func (c Condition) SpecString(key, def string) string {
	if v, ok := c.Spec[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return def
}

// SpecBool reads a bool field from Spec with a default.
func (c Condition) SpecBool(key string, def bool) bool {
	if v, ok := c.Spec[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return def
}

// SpecDuration reads a duration field from Spec. Accepts Go duration
// strings ("5s", "10m") or integer seconds.
func (c Condition) SpecDuration(key string, def time.Duration) time.Duration {
	if v, ok := c.Spec[key]; ok {
		switch n := v.(type) {
		case string:
			if d, err := time.ParseDuration(n); err == nil {
				return d
			}
		case float64:
			return time.Duration(n) * time.Second
		case int:
			return time.Duration(n) * time.Second
		case int64:
			return time.Duration(n) * time.Second
		}
	}
	return def
}

// ─── Action ────────────────────────────────────────────────────

// ActionType enumerates the built-in action categories.
type ActionType string

const (
	// ActionSlashCmd — invokes a CLI slash command via the command handler.
	ActionSlashCmd ActionType = "slash_cmd"
	// ActionShell — runs a shell command under CoderMode safety.
	ActionShell ActionType = "shell"
	// ActionAgentTask — boots a ReAct loop with the given task.
	ActionAgentTask ActionType = "agent_task"
	// ActionWorkerDispatch — single-agent worker invocation.
	ActionWorkerDispatch ActionType = "worker_dispatch"
	// ActionLLMPrompt — headless LLM call (no tool loop).
	ActionLLMPrompt ActionType = "llm_prompt"
	// ActionWebhook — HTTP POST.
	ActionWebhook ActionType = "webhook"
	// ActionHook — fires a chatcli hook by name.
	ActionHook ActionType = "hook"
	// ActionNoop — do nothing (testing, placeholders).
	ActionNoop ActionType = "noop"
)

// Action describes what to execute when the schedule fires (and the
// wait condition, if any, is satisfied).
type Action struct {
	Type    ActionType     `json:"type"`
	Payload map[string]any `json:"payload,omitempty"`
}

// Validate returns a non-nil error when the Action is malformed. Type-
// specific validation (e.g. URL shape for webhook) is the executor's
// responsibility.
func (a Action) Validate() error {
	if strings.TrimSpace(string(a.Type)) == "" {
		return fmt.Errorf("action.type is required")
	}
	return nil
}

// PayloadString reads a string field from the action payload with a default.
func (a Action) PayloadString(key, def string) string {
	if v, ok := a.Payload[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return def
}

// PayloadInt reads an int field from the action payload.
func (a Action) PayloadInt(key string, def int) int {
	if v, ok := a.Payload[key]; ok {
		switch n := v.(type) {
		case int:
			return n
		case int64:
			return int(n)
		case float64:
			return int(n)
		}
	}
	return def
}

// PayloadBool reads a bool field from the action payload.
func (a Action) PayloadBool(key string, def bool) bool {
	if v, ok := a.Payload[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return def
}

// PayloadStringSlice reads a []string field (or a single string that
// splits on commas/whitespace).
func (a Action) PayloadStringSlice(key string) []string {
	v, ok := a.Payload[key]
	if !ok {
		return nil
	}
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		// Split on commas; tolerate whitespace.
		parts := strings.FieldsFunc(x, func(r rune) bool {
			return r == ',' || r == ';' || r == '\n'
		})
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	return nil
}

// PayloadMap reads a map[string]any field. Useful for webhook headers
// and llm_prompt tool overrides.
func (a Action) PayloadMap(key string) map[string]any {
	v, ok := a.Payload[key]
	if !ok {
		return nil
	}
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

// ─── Marshaling helpers ────────────────────────────────────────

// mustMarshal is used by the WAL/audit/IPC layers where encoding
// failure means a schema bug — not a runtime condition the scheduler
// is expected to survive. Panic here is preferable to silently
// writing a truncated record.
//
// Callers that want to handle the error themselves should use
// json.Marshal directly.
func mustMarshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("scheduler: internal marshal failure: %v", err))
	}
	return b
}
