/*
 * Park / resume integration for the interactive AgentMode loop.
 *
 * This file owns three concerns kept separate from agent_mode.go's huge
 * Run() body:
 *
 *   1. handleAgentPark — called from the tool-dispatch loop when a tool
 *      returns the park sentinel. It snapshots the loop state, schedules
 *      the appropriate resume job, prints the park banner, and returns
 *      a sentinel to bubble cleanly out of Run().
 *
 *   2. RunResumed — public re-entry point used when the scheduler fires
 *      AgentResume. Restores the snapshot's history and synthesizes the
 *      tool result the @park call would have produced, then drives
 *      processAIResponseAndAct from the same loop position.
 *
 *   3. enqueueParkResumeJob / enqueueParkPollJob — small wrappers that
 *      build the right scheduler.Job for each ParkRequest mode and
 *      submit it via the scheduler adapter.
 */
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/cli/agent/park"
	"github.com/diillson/chatcli/cli/scheduler"
	"github.com/diillson/chatcli/i18n"
	llmclient "github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// errAgentParkedRequested is the loop-internal sentinel processAIResponseAndAct
// returns when a tool asked to suspend. The caller in Run() (and the
// tests) match on this and return nil to the user — park is success.
var errAgentParkedRequested = errors.New("agent parked: suspending loop until scheduler resumes")

// handleAgentPark snapshots the agent's state, enqueues the resume job
// (or polling job), publishes the banner, and returns the sentinel.
//
// The history captured here already contains the user's original query
// and any additional context (Run() appends them at line 652). The
// system prompt is the first system message in history; isCoderMode
// tells resume which prompt mode to honor.
//
// pendingToolCallID / pendingToolName describe the still-open native
// tool_use entry that the @park invocation occupied. They are empty for
// XML-mode parks.
func (a *AgentMode) handleAgentPark(
	ctx context.Context,
	req park.Request,
	pendingToolCallID string,
	pendingToolName string,
) error {
	snap := &park.Snapshot{
		Token:           park.NewToken(),
		History:         append([]models.Message(nil), a.cli.history...),
		AgentsLaunched:  a.agentsLaunched,
		ToolCallsExecd:  a.toolCallsExecd,
		IsCoderMode:     a.isCoderMode,
		IsOneShot:       a.isOneShot,
		Provider:        a.cli.Provider,
		Model:           a.cli.Model,
		SkillModelHint:  a.skillModelHint,
		SkillEffortHint: a.skillEffortHint,
		Park:            req,
	}
	// Carry the pending native tool_use ID through the snapshot so
	// resume can synthesize a matching tool_result and avoid Anthropic's
	// strict-pairing rejection.
	snap.PendingToolCallID = pendingToolCallID
	snap.PendingToolName = pendingToolName
	if err := snap.Save(); err != nil {
		return fmt.Errorf("park: save snapshot: %w", err)
	}

	// Enqueue the matching scheduler job. For pure timer parks we go
	// straight to AgentResume; for polling parks we use ParkPoll which
	// fans out to AgentResume on success/timeout.
	jobID, err := a.enqueueParkJob(ctx, snap)
	if err != nil {
		// Cleanup: a snapshot without a scheduler job is dead weight.
		_ = park.Delete(snap.Token)
		return fmt.Errorf("park: enqueue resume job: %w", err)
	}
	snap.SchedulerJobID = jobID
	if err := snap.Save(); err != nil {
		// Best-effort: snapshot still works at resume time without the
		// job id (the cancel path falls back to listing).
		a.logger.Warn("park: re-save snapshot with job id failed",
			zap.String("token", snap.Token), zap.Error(err))
	}

	// Banner — informs user via stdout. The dispatch loop's trailing
	// stdout writes have already cleared, so this stands out.
	fmt.Println()
	fmt.Println(colorize("  🅿️  "+i18n.T("park.banner.scheduled",
		snap.Token, parkRequestETA(req), describeParkRequest(req)),
		ColorCyan+ColorBold))
	fmt.Println(colorize("     "+i18n.T("park.banner.hint", snap.Token, snap.Token), ColorGray))
	fmt.Println()

	// Bus event so /jobs and /parked stay coherent (the scheduler bridge
	// owns publication; we route through it instead of the scheduler
	// directly to keep all UI fan-out paths consistent).
	if a.cli.schedulerBridge != nil {
		a.cli.schedulerBridge.PublishEvent(scheduler.NewEvent("park.scheduled").
			WithMessage(snap.Token).
			WithData("mode", string(req.Mode)).
			WithData("eta", parkRequestETA(req)))
	}

	a.logger.Info("agent parked",
		zap.String("token", snap.Token),
		zap.String("mode", string(req.Mode)),
		zap.String("scheduler_job_id", jobID))

	return errAgentParkedRequested
}

// enqueueParkJob constructs and submits the scheduler job that will
// eventually fire AgentResume for the given snapshot. Polling parks go
// through ParkPoll first.
func (a *AgentMode) enqueueParkJob(ctx context.Context, snap *park.Snapshot) (string, error) {
	if a.cli.scheduler == nil {
		return "", errors.New("park: scheduler not initialized")
	}
	owner := scheduler.Owner{
		Kind: "park",
		ID:   "agent",
		Tag:  snap.Token,
	}

	now := time.Now()
	req := snap.Park

	if req.IsPolling() {
		mode := "for_url"
		if req.Mode == park.ModeForCmd {
			mode = "for_cmd"
		}
		payload := map[string]any{
			"resume_token":  snap.Token,
			"mode":          mode,
			"interval":      req.Interval.String(),
			"deadline_unix": req.Deadline.Unix(),
			"success_when":  req.SuccessWhen,
		}
		if mode == "for_url" {
			payload["url"] = req.URL
			payload["method"] = req.HTTPMethod
			if len(req.HTTPHeaders) > 0 {
				h := make(map[string]any, len(req.HTTPHeaders))
				for k, v := range req.HTTPHeaders {
					h[k] = v
				}
				payload["headers"] = h
			}
		} else {
			payload["command"] = req.Command
		}
		job := scheduler.NewJob(
			"park-poll:"+snap.Token,
			owner,
			scheduler.Schedule{Kind: scheduler.ScheduleRelative, Relative: req.Interval},
			scheduler.Action{Type: scheduler.ActionParkPoll, Payload: payload},
		)
		// Park is interactively user-approved before this code runs:
		// the agent's security check at cli/agent_mode.go fired the
		// [y]/[a]/[n]/[d] prompt with the FULL @park args (including
		// the embedded url / cmd) on screen. A 'y' there is the user
		// pre-authorizing the polling probe to run the command they
		// just saw. Propagate as DangerousConfirmed so the fire-time
		// re-check in RunShell admits the cmd without a second prompt
		// (which would never come through — the scheduler dispatcher
		// has no human attached). Denylist still wins; an Ask-classed
		// command the user approved interactively still runs because
		// they explicitly said yes; a Deny-classed one is rejected at
		// fire time regardless.
		job.DangerousConfirmed = true
		job.Description = "agent park polling — " + describeParkRequest(req)
		out, err := a.cli.scheduler.Enqueue(ctx, job)
		if err != nil {
			return "", err
		}
		return string(out.ID), nil
	}

	// Timer / wallclock parks — direct AgentResume.
	var sched scheduler.Schedule
	if req.Mode == park.ModeDelay {
		sched = scheduler.Schedule{Kind: scheduler.ScheduleRelative, Relative: req.Delay}
	} else {
		sched = scheduler.Schedule{Kind: scheduler.ScheduleAbsolute, ExactTime: req.Until}
	}
	job := scheduler.NewJob(
		"park-resume:"+snap.Token,
		owner,
		sched,
		scheduler.Action{
			Type: scheduler.ActionAgentResume,
			Payload: map[string]any{
				"resume_token": snap.Token,
				"outcome":      "elapsed",
				"detail":       fmt.Sprintf("scheduled park elapsed at %s", req.FireAt(now).Format(time.RFC3339)),
			},
		},
	)
	job.Description = "agent park timer — " + describeParkRequest(req)
	out, err := a.cli.scheduler.Enqueue(ctx, job)
	if err != nil {
		return "", err
	}
	return string(out.ID), nil
}

// describeParkRequest returns the short user-facing description.
func describeParkRequest(r park.Request) string {
	switch r.Mode {
	case park.ModeDelay:
		if r.Note != "" {
			return fmt.Sprintf("delay %s — %s", r.Delay, r.Note)
		}
		return fmt.Sprintf("delay %s", r.Delay)
	case park.ModeUntil:
		return fmt.Sprintf("until %s", r.Until.Format(time.RFC3339))
	case park.ModeForURL:
		if r.Note != "" {
			return fmt.Sprintf("polling %s every %s — %s", r.URL, r.Interval, r.Note)
		}
		return fmt.Sprintf("polling %s every %s", r.URL, r.Interval)
	case park.ModeForCmd:
		cmd := r.Command
		if len(cmd) > 60 {
			cmd = cmd[:60] + "…"
		}
		if r.Note != "" {
			return fmt.Sprintf("polling %q every %s — %s", cmd, r.Interval, r.Note)
		}
		return fmt.Sprintf("polling %q every %s", cmd, r.Interval)
	}
	return string(r.Mode)
}

// parkRequestETA returns a short ETA string for the banner.
func parkRequestETA(r park.Request) string {
	now := time.Now()
	switch r.Mode {
	case park.ModeDelay:
		return now.Add(r.Delay).Format("15:04:05")
	case park.ModeUntil:
		return r.Until.Format("15:04:05")
	case park.ModeForURL, park.ModeForCmd:
		return r.Deadline.Format("15:04:05") + " (deadline)"
	}
	return "—"
}

// RunResumed re-enters the agent loop using a previously-saved snapshot.
// It is idempotent on token: a Resume that lost the race against a
// /cancel-park finds a missing snapshot and returns ErrSnapshotNotFound,
// which the caller renders as a no-op.
//
// outcome and detail come from the AgentResume action payload. They are
// woven into the synthetic tool-result message so the LLM sees:
//
//	[park completed] outcome=matched detail=<probe response>
func (a *AgentMode) RunResumed(ctx context.Context, snap *park.Snapshot, outcome, detail string) error {
	if !a.runInflight.CompareAndSwap(false, true) {
		return fmt.Errorf("agent: another Run is already in flight on this AgentMode instance")
	}
	defer a.runInflight.Store(false)

	a.logger.Info("agent resuming from park",
		zap.String("token", snap.Token),
		zap.String("outcome", outcome),
		zap.String("mode", string(snap.Park.Mode)))

	// Restore loop state. We keep the LLM client / model decisions where
	// they were at park time so a config change between park and resume
	// doesn't silently re-route the conversation to a different model.
	a.isCoderMode = snap.IsCoderMode
	a.isOneShot = snap.IsOneShot
	a.agentsLaunched = snap.AgentsLaunched
	a.toolCallsExecd = snap.ToolCallsExecd
	if snap.SkillModelHint != "" {
		a.skillModelHint = snap.SkillModelHint
	}
	if snap.SkillEffortHint != llmclient.EffortUnset {
		a.skillEffortHint = snap.SkillEffortHint
	}
	a.cli.history = append([]models.Message(nil), snap.History...)

	// Synthesize the tool_result message that closes the @park tool call.
	// Native (Anthropic-style) requires Role=tool with the matching
	// ToolCallID; XML mode uses a Role=user batch-format message —
	// matching the existing dispatch loop's append at the batch boundary.
	resultText := buildParkResumeMessage(snap.Park, outcome, detail)

	if snap.PendingToolCallID != "" {
		a.cli.history = append(a.cli.history, models.Message{
			Role:       "tool",
			ToolCallID: snap.PendingToolCallID,
			Content:    resultText,
		})
	} else {
		a.cli.history = append(a.cli.history, models.Message{
			Role: "user",
			Content: i18n.T("agent.feedback.tool_output", "park_resume",
				fmt.Sprintf("--- Resultado da Ação 1 (@park) ---\n%s\n", resultText)),
		})
	}

	// Banner so the user sees the resume start in their terminal.
	fmt.Println()
	fmt.Println(colorize("  ▶️  "+i18n.T("park.banner.resumed", snap.Token, outcome), ColorGreen+ColorBold))
	fmt.Println()

	// Audit checkpoint and snapshot bookkeeping. We keep the snapshot
	// file on disk (with LastResumeAt updated) for forensic purposes;
	// scheduled cleanup removes stale ones via Sweep.
	snap.LastResumeAt = time.Now().UTC()
	if err := snap.Save(); err != nil {
		a.logger.Warn("park: failed to update LastResumeAt", zap.String("token", snap.Token), zap.Error(err))
	}

	// Reuse the same processAIResponseAndAct loop. maxTurns is fresh —
	// the resumed loop gets its own turn budget so a long park doesn't
	// borrow turns from the original Run.
	maxTurns := AgentMaxTurns()
	err := a.processAIResponseAndAct(ctx, maxTurns)

	// Successful completion (or any non-park error): retire the snapshot.
	if !errors.Is(err, errAgentParkedRequested) {
		_ = park.Delete(snap.Token)
	}
	return err
}

// buildParkResumeMessage builds the synthetic tool result the LLM sees
// at resume time. Concise and structured so the model can pattern-match.
func buildParkResumeMessage(req park.Request, outcome, detail string) string {
	var sb strings.Builder
	sb.WriteString("[@park completed]\n")
	sb.WriteString("mode: ")
	sb.WriteString(string(req.Mode))
	sb.WriteString("\noutcome: ")
	sb.WriteString(outcome)
	if req.Note != "" {
		sb.WriteString("\nnote: ")
		sb.WriteString(req.Note)
	}
	if detail != "" {
		sb.WriteString("\n--- detail ---\n")
		sb.WriteString(detail)
	}
	sb.WriteString("\n\nContinue from where you stopped. Use the detail above to inform your next step.")
	return sb.String()
}

// quietColorize is a thin alias so this file does not depend on private
// symbols of agent_mode.go that may move between refactors. The color
// constants live in cli/cli.go (ColorCyan etc.) which we already import
// implicitly via package cli.
var _ = agent.ColorCyan // keep agent import live (used in other helpers)

// _ = os.Stderr keeps the os import live when conditional logs disappear.
var _ = os.Stderr
