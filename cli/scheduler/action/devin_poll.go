/*
 * DevinPoll — durable watcher for a Devin (Cognition) session.
 *
 * The action runs once per scheduler firing: it fetches the session via the
 * Devin API (v1 individual/Teams or v3 organizations/enterprise — resolved
 * from the DEVIN_* environment at fire time, so no credential is ever
 * persisted in the WAL) and either:
 *
 *   - turn boundary reached (waiting for user / finished) → completes with
 *     the session state and Devin's latest reply as the job output
 *   - session errored/expired → fails the job with the API detail
 *   - still working → re-enqueues itself for `now + interval`
 *   - deadline elapsed → completes with a timeout note (the session keeps
 *     running server-side; only the watch stops)
 *
 * Self-rescheduling keeps each firing bounded; crash-resilience falls out of
 * the scheduler WAL, exactly like ParkPoll.
 *
 * Payload:
 *
 *   session_id     string (required) Devin session id
 *   interval       string (required) poll cadence (time.ParseDuration)
 *   deadline_unix  int64  (required) absolute watch deadline
 */
package action

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/scheduler"
	"github.com/diillson/chatcli/llm/devin"
	"go.uber.org/zap"
)

// devinPollNewAPI builds the Devin API client from the environment at fire
// time. Overridable in tests.
var devinPollNewAPI = func() (devin.API, error) {
	return devin.NewAPI(devin.ResolveAPIConfigFromEnv(zap.NewNop()))
}

// DevinPoll implements scheduler.ActionExecutor.
type DevinPoll struct{}

// NewDevinPoll builds the executor.
func NewDevinPoll() *DevinPoll { return &DevinPoll{} }

// Type returns the canonical ActionType literal.
func (DevinPoll) Type() scheduler.ActionType { return scheduler.ActionDevinPoll }

// ValidateSpec enforces the payload invariants at admission time.
func (DevinPoll) ValidateSpec(payload map[string]any) error {
	if id, _ := payload["session_id"].(string); strings.TrimSpace(id) == "" {
		return fmt.Errorf("devin_poll: payload.session_id is required")
	}
	if i, _ := payload["interval"].(string); strings.TrimSpace(i) == "" {
		return fmt.Errorf("devin_poll: interval is required")
	}
	switch v := payload["deadline_unix"].(type) {
	case int, int64, float64:
		_ = v
	default:
		return fmt.Errorf("devin_poll: deadline_unix is required (int unix seconds)")
	}
	return nil
}

// Execute runs one poll iteration and either completes the watch or
// re-enqueues itself for the next interval.
func (p DevinPoll) Execute(ctx context.Context, action scheduler.Action, env *scheduler.ExecEnv) scheduler.ActionResult {
	if env == nil {
		return scheduler.ActionResult{Err: fmt.Errorf("devin_poll: no exec env")}
	}
	sessionID := action.PayloadString("session_id", "")
	deadline := time.Unix(payloadInt64(action, "deadline_unix"), 0)

	api, err := devinPollNewAPI()
	if err != nil {
		// Missing credentials are permanent: retrying cannot fix them.
		return scheduler.ActionResult{Err: fmt.Errorf("devin_poll: %w", err)}
	}

	probeCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	session, err := api.GetSession(probeCtx, sessionID)
	if err != nil {
		if time.Now().After(deadline) {
			return scheduler.ActionResult{Err: fmt.Errorf("devin_poll: deadline exceeded while probing %s: %w", sessionID, err)}
		}
		// Transient probe failure: reschedule and note it.
		return p.rescheduleSelf(ctx, action, env, "probe error: "+err.Error())
	}

	switch {
	case session.State == devin.StateError || session.State == devin.StateExpired:
		detail := session.StatusDetail
		if detail == "" {
			detail = session.RawStatus
		}
		return scheduler.ActionResult{Err: fmt.Errorf("devin_poll: session %s ended in state %s (%s)", sessionID, session.State, detail)}

	case session.State.TurnBoundary():
		reply := devin.CollectReply(probeCtx, api, session, nil)
		return scheduler.ActionResult{Output: devinPollOutput(session, reply)}
	}

	if time.Now().After(deadline) {
		// The watch expires; the session keeps running server-side.
		return scheduler.ActionResult{Output: fmt.Sprintf("devin_poll: watch deadline elapsed; session %s still %s — %s", sessionID, session.State, session.URL)}
	}
	return p.rescheduleSelf(ctx, action, env, fmt.Sprintf("session %s still %s", sessionID, session.State))
}

// devinPollOutput renders the final watch output (job result in /jobs).
func devinPollOutput(session *devin.Session, reply string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "devin session %s reached %s", session.ID, session.State)
	if session.URL != "" {
		fmt.Fprintf(&b, " — %s", session.URL)
	}
	for _, pr := range session.PullRequests {
		b.WriteString("\nPR: " + pr.URL)
		if pr.State != "" {
			b.WriteString(" (" + pr.State + ")")
		}
	}
	if reply != "" {
		if len(reply) > 8192 {
			reply = reply[:8192] + "\n…[truncated]…"
		}
		b.WriteString("\n\n" + reply)
	}
	return b.String()
}

// rescheduleSelf enqueues another DevinPoll iteration.
func (p DevinPoll) rescheduleSelf(ctx context.Context, action scheduler.Action, env *scheduler.ExecEnv, reason string) scheduler.ActionResult {
	interval := payloadDuration(action, "interval", 30*time.Second)
	job := scheduler.NewJob(
		"devin-poll:"+action.PayloadString("session_id", ""),
		env.Job.Owner,
		scheduler.Schedule{Kind: scheduler.ScheduleRelative, Relative: interval},
		action,
	)
	job.DangerousConfirmed = env.Job.DangerousConfirmed
	if env.Enqueue == nil {
		return scheduler.ActionResult{Err: fmt.Errorf("devin_poll: scheduler enqueue not wired")}
	}
	if _, err := env.Enqueue(ctx, job); err != nil {
		return scheduler.ActionResult{Err: fmt.Errorf("devin_poll: re-enqueue: %w", err)}
	}
	return scheduler.ActionResult{Output: "devin_poll: rescheduled — " + reason, Transient: true}
}
