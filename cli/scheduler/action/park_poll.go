/*
 * ParkPoll — drives the @park for_url / for_cmd polling loop.
 *
 * The action runs once per scheduler firing. It probes the target
 * (HTTP via the bridge or shell via RunShell), evaluates SuccessWhen,
 * and either:
 *
 *   - matches  → enqueue a one-shot AgentResume (outcome=matched) and
 *                finalize the recurring job via StopRecurrence
 *   - timeouts → enqueue a one-shot AgentResume (outcome=timeout) and
 *                finalize the recurring job via StopRecurrence
 *   - waits    → return success so the dispatcher re-arms the SAME job
 *                for the next interval cycle
 *
 * The poll job is a recurring interval schedule re-armed in place: one
 * JobID and one name-index entry across every cycle. Re-enqueueing a
 * sibling job under the same name (the original design) is rejected by
 * the scheduler's duplicate-name admission while the current cycle is
 * still Running — the chain died on the first non-matching probe.
 * Sibling re-scheduling survives only as a fallback for legacy one-shot
 * relative jobs replayed from a pre-upgrade WAL, with a per-iteration
 * unique name.
 *
 * Each firing stays bounded and idempotent; a single fire never blocks
 * beyond the per-iteration HTTP/shell timeout. Crash-resilience falls
 * out of the scheduler WAL: an incomplete poll iteration is replayed on
 * restart.
 *
 * Failure is never silent: if the poll chain cannot continue (legacy
 * re-enqueue rejected) or the resume job cannot be admitted, the
 * executor falls back to waking the agent directly through
 * NotifyParkComplete so the user always hears back from a park.
 *
 * Payload (all strings are durations parseable by time.ParseDuration
 * unless noted):
 *
 *   resume_token   string         (required) snapshot identifier
 *   mode           string         (required) "for_url" | "for_cmd"
 *   url            string         for_url only
 *   method         string         for_url, default "GET"
 *   headers        map[string]any for_url, optional
 *   command        string         for_cmd only
 *   interval       string         (required) poll cadence
 *   deadline_unix  int64          (required) absolute deadline
 *   success_when   string         optional matcher
 *   probe_timeout  string         optional per-probe timeout (default 30s)
 *
 * Why deadline_unix instead of a duration: the action re-enqueues
 * itself across many firings, so we must store the absolute deadline,
 * not a relative duration that would silently slide.
 */
package action

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/scheduler"
)

// ParkPoll implements scheduler.ActionExecutor.
type ParkPoll struct{}

// NewParkPoll builds the executor.
func NewParkPoll() *ParkPoll { return &ParkPoll{} }

// Type returns the canonical ActionType literal.
func (ParkPoll) Type() scheduler.ActionType { return scheduler.ActionParkPoll }

// ValidateSpec enforces the per-mode invariants at admission time.
func (ParkPoll) ValidateSpec(payload map[string]any) error {
	tok, _ := payload["resume_token"].(string)
	if strings.TrimSpace(tok) == "" {
		return fmt.Errorf("park_poll: payload.resume_token is required")
	}
	mode, _ := payload["mode"].(string)
	switch mode {
	case "for_url":
		if u, _ := payload["url"].(string); strings.TrimSpace(u) == "" {
			return fmt.Errorf("park_poll: url is required for mode=for_url")
		}
	case "for_cmd":
		if c, _ := payload["command"].(string); strings.TrimSpace(c) == "" {
			return fmt.Errorf("park_poll: command is required for mode=for_cmd")
		}
	default:
		return fmt.Errorf("park_poll: unknown mode %q", mode)
	}
	if i, _ := payload["interval"].(string); strings.TrimSpace(i) == "" {
		return fmt.Errorf("park_poll: interval is required")
	}
	switch v := payload["deadline_unix"].(type) {
	case int, int64, float64:
		_ = v
	default:
		return fmt.Errorf("park_poll: deadline_unix is required (int unix seconds)")
	}
	return nil
}

// Execute runs one poll iteration and either fans out to AgentResume or
// re-enqueues itself for the next interval. Errors flagged Transient are
// retried by the scheduler's standard retry policy; structural errors
// (no bridge, malformed payload) are permanent.
func (p ParkPoll) Execute(ctx context.Context, action scheduler.Action, env *scheduler.ExecEnv) scheduler.ActionResult {
	if env == nil || env.Bridge == nil {
		return scheduler.ActionResult{Err: fmt.Errorf("park_poll: no bridge wired")}
	}

	token := action.PayloadString("resume_token", "")
	mode := action.PayloadString("mode", "")
	successWhen := action.PayloadString("success_when", "")

	deadlineUnix := payloadInt64(action, "deadline_unix")
	deadline := time.Unix(deadlineUnix, 0)
	now := time.Now()

	// Deadline elapsed: fire AgentResume with timeout outcome and stop.
	if now.After(deadline) {
		return p.fireResume(ctx, env, token, "timeout", fmt.Sprintf("deadline exceeded at %s", now.Format(time.RFC3339)))
	}

	// One-shot per-probe timeout. Defaults to 30s; bounded by remaining
	// time-to-deadline so the probe never overshoots.
	probeTimeout := payloadDuration(action, "probe_timeout", 30*time.Second)
	if remaining := time.Until(deadline); remaining < probeTimeout {
		probeTimeout = remaining
	}

	// Run the probe, classify, and decide.
	matched, summary, runErr := p.probe(ctx, env, action, mode, probeTimeout)
	if runErr != nil {
		// Probe failure: keep the cycle alive and try again on the
		// next interval — the absolute deadline above caps how long a
		// broken probe can spin before AgentResume fires with timeout.
		return p.continuePolling(ctx, action, env, "probe error: "+runErr.Error())
	}

	if matchSuccessWhen(successWhen, summary, matched) {
		return p.fireResume(ctx, env, token, "matched", summary.detail())
	}

	return p.continuePolling(ctx, action, env, "probe did not match yet: "+summary.short())
}

// continuePolling keeps the poll chain alive for one more interval. On
// the recurring (interval) schedule this is simply a successful result —
// the dispatcher re-arms the same job. Legacy one-shot relative jobs
// replayed from a pre-upgrade WAL fall back to sibling re-scheduling.
func (p ParkPoll) continuePolling(ctx context.Context, action scheduler.Action, env *scheduler.ExecEnv, reason string) scheduler.ActionResult {
	if strings.HasPrefix(env.Job.Type, string(scheduler.ScheduleInterval)) {
		return scheduler.ActionResult{Output: "park_poll: waiting — " + reason}
	}
	return p.rescheduleSelf(ctx, action, env, reason)
}

// probeSummary captures the fields evaluated by SuccessWhen.
type probeSummary struct {
	HTTPStatus int
	ExitCode   int
	Body       string
}

// short returns a one-line description suitable for an audit message.
func (s probeSummary) short() string {
	if s.HTTPStatus != 0 {
		preview := s.Body
		if len(preview) > 80 {
			preview = preview[:80] + "…"
		}
		return fmt.Sprintf("status=%d body=%q", s.HTTPStatus, preview)
	}
	preview := s.Body
	if len(preview) > 80 {
		preview = preview[:80] + "…"
	}
	return fmt.Sprintf("exit=%d body=%q", s.ExitCode, preview)
}

// detail returns the verbatim probe output for the resume context. The
// agent uses this as the synthetic tool-call result, so length-cap to
// keep token cost bounded.
func (s probeSummary) detail() string {
	body := s.Body
	if len(body) > 8192 {
		body = body[:8192] + "\n…[truncated]…"
	}
	if s.HTTPStatus != 0 {
		return fmt.Sprintf("HTTP %d\n%s", s.HTTPStatus, body)
	}
	return fmt.Sprintf("exit=%d\n%s", s.ExitCode, body)
}

// probe issues the actual probe (HTTP via bridge, shell via RunShell)
// and returns a probeSummary plus a "default success" flag (whether the
// probe would succeed under an EMPTY success_when).
func (p ParkPoll) probe(ctx context.Context, env *scheduler.ExecEnv, action scheduler.Action, mode string, timeout time.Duration) (defaultMatched bool, summary probeSummary, err error) {
	switch mode {
	case "for_url":
		url := action.PayloadString("url", "")
		method := strings.ToUpper(action.PayloadString("method", "GET"))
		headers := payloadStringMap(action, "headers")
		status, body, herr := env.Bridge.RunHTTPProbe(ctx, url, method, headers, timeout)
		if herr != nil {
			return false, probeSummary{}, herr
		}
		summary = probeSummary{HTTPStatus: status, Body: body}
		// Empty success_when means "any 2xx".
		return status >= 200 && status < 300, summary, nil

	case "for_cmd":
		cmd := action.PayloadString("command", "")
		// Shell parking inherits the job's DangerousConfirmed bit so
		// pre-authorized parks (--i-know upstream) keep working at fire.
		dangerous := env.Job.DangerousConfirmed
		probeCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		stdout, stderr, exit, cerr := env.Bridge.RunShell(probeCtx, cmd, nil, false, dangerous)
		if cerr != nil && exit == 0 && stdout == "" && stderr == "" {
			// True I/O failure (couldn't even start the process).
			return false, probeSummary{}, cerr
		}
		body := stdout
		if stderr != "" {
			body += "\n" + stderr
		}
		summary = probeSummary{ExitCode: exit, Body: body}
		// Empty success_when means "exit 0".
		return exit == 0, summary, nil
	}
	return false, probeSummary{}, fmt.Errorf("park_poll: unsupported mode %q", mode)
}

// matchSuccessWhen evaluates the SuccessWhen DSL against a probeSummary.
//
//	""                          → defaultMatched
//	"status=200"                → exact HTTP status
//	"status=200..299"           → HTTP status in [lo,hi]
//	"exit=0"                    → exact shell exit code
//	"body contains:foo"         → substring on body
//	"body matches:^OK$"         → regexp on body (anchored if author wants)
func matchSuccessWhen(expr string, summary probeSummary, defaultMatched bool) bool {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return defaultMatched
	}
	parts := strings.SplitN(expr, "=", 2)
	if len(parts) == 2 {
		key := strings.ToLower(strings.TrimSpace(parts[0]))
		val := strings.TrimSpace(parts[1])
		switch key {
		case "status":
			return matchNumberOrRange(summary.HTTPStatus, val)
		case "exit":
			return matchNumberOrRange(summary.ExitCode, val)
		}
	}
	if rest, ok := strings.CutPrefix(expr, "body contains:"); ok {
		return strings.Contains(summary.Body, rest)
	}
	if rest, ok := strings.CutPrefix(expr, "body matches:"); ok {
		re, err := regexp.Compile(rest)
		if err != nil {
			return false
		}
		return re.MatchString(summary.Body)
	}
	// Fallback: treat as substring on body — common LLM phrasing.
	return strings.Contains(summary.Body, expr)
}

// matchNumberOrRange supports "200" or "200..299".
func matchNumberOrRange(actual int, spec string) bool {
	if lo, hi, ok := strings.Cut(spec, ".."); ok {
		l, errL := strconv.Atoi(strings.TrimSpace(lo))
		h, errH := strconv.Atoi(strings.TrimSpace(hi))
		if errL != nil || errH != nil {
			return false
		}
		return actual >= l && actual <= h
	}
	want, err := strconv.Atoi(strings.TrimSpace(spec))
	if err != nil {
		return false
	}
	return actual == want
}

// fireResume queues a one-shot AgentResume job that the scheduler will
// fire immediately. Returns a successful ActionResult with
// StopRecurrence set so the recurring poll job finalizes instead of
// re-arming another cycle.
//
// Schedule must be positive — Schedule.Validate rejects Relative=0 — so
// we use 1ms which the dispatcher rounds to "due now" on the next tick
// of the queue (typically <100ms). Effectively immediate.
//
// A duplicate-name rejection means an identically-named resume job is
// already live (a retried closing cycle) — the wake-up is on its way,
// so it counts as success. Any other admission failure falls back to
// waking the agent directly through the bridge: a park must never end
// without the user hearing back.
func (ParkPoll) fireResume(ctx context.Context, env *scheduler.ExecEnv, token, outcome, detail string) scheduler.ActionResult {
	done := scheduler.ActionResult{
		Output:         fmt.Sprintf("resume fired: outcome=%s", outcome),
		StopRecurrence: true,
	}
	if env.Enqueue == nil {
		return scheduler.ActionResult{Err: fmt.Errorf("park_poll: scheduler enqueue not wired")}
	}
	job := scheduler.NewJob(
		"park-resume:"+token,
		env.Job.Owner,
		scheduler.Schedule{Kind: scheduler.ScheduleRelative, Relative: time.Millisecond},
		scheduler.Action{
			Type: scheduler.ActionAgentResume,
			Payload: map[string]any{
				"resume_token": token,
				"outcome":      outcome,
				"detail":       detail,
			},
		},
	)
	job.DangerousConfirmed = env.Job.DangerousConfirmed
	_, err := env.Enqueue(ctx, job)
	if err == nil || errors.Is(err, scheduler.ErrDuplicateName) {
		return done
	}
	if env.Bridge != nil {
		if nerr := env.Bridge.NotifyParkComplete(ctx, token, outcome, detail); nerr == nil {
			done.Output += " (direct notify; resume job rejected: " + err.Error() + ")"
			return done
		}
	}
	return scheduler.ActionResult{Err: fmt.Errorf("park_poll: enqueue resume: %w", err)}
}

// rescheduleSelf enqueues a sibling ParkPoll iteration. Legacy path for
// one-shot relative jobs replayed from a pre-upgrade WAL — recurring
// jobs re-arm in place and never come through here. The sibling gets a
// per-iteration unique name because the scheduler's duplicate-name
// admission rejects the base name while the current cycle is still
// Running. If the sibling cannot be admitted, the chain wakes the agent
// with an error outcome instead of dying silently.
func (p ParkPoll) rescheduleSelf(ctx context.Context, action scheduler.Action, env *scheduler.ExecEnv, reason string) scheduler.ActionResult {
	token := action.PayloadString("resume_token", "")
	if env.Enqueue == nil {
		return scheduler.ActionResult{Err: fmt.Errorf("park_poll: scheduler enqueue not wired")}
	}

	// Copy the payload before bumping the iteration counter — the map
	// is shared with the running job, which the scheduler may be
	// serializing to the WAL concurrently.
	iteration := payloadInt64(action, "iteration") + 1
	payload := make(map[string]any, len(action.Payload)+1)
	for k, v := range action.Payload {
		payload[k] = v
	}
	payload["iteration"] = iteration

	interval := payloadDuration(action, "interval", 30*time.Second)
	job := scheduler.NewJob(
		fmt.Sprintf("park-poll:%s.i%d", token, iteration),
		env.Job.Owner,
		scheduler.Schedule{Kind: scheduler.ScheduleRelative, Relative: interval},
		scheduler.Action{Type: action.Type, Payload: payload},
	)
	job.DangerousConfirmed = env.Job.DangerousConfirmed
	if _, err := env.Enqueue(ctx, job); err != nil {
		return p.fireResume(ctx, env, token, "error",
			fmt.Sprintf("park polling aborted: could not schedule the next probe (%s); last state: %s", err, reason))
	}
	return scheduler.ActionResult{Output: "park_poll: rescheduled — " + reason}
}
