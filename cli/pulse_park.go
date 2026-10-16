/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Live telemetry for park and resume. Parking is a point under the agent
 * that chose to sleep, carrying why (the park mode), how long until it is
 * due and the scheduler job that will wake it. A resume is a span hung
 * from that job, so the dashboard reads park → job → resume as one chain.
 * The URL, the command, the note and the probe output never travel.
 */
package cli

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/diillson/chatcli/cli/agent/park"
	"github.com/diillson/chatcli/cli/scheduler"
	"github.com/diillson/chatcli/pkg/pulse"
)

// Dashboard nodes of park and resume.
const (
	pulseParkNode   = "park"
	pulseResumeNode = "park:resume"
)

// pulseParkModes are the park modes reported as they are; anything else
// reads as "other", so the attribute stays a closed enum.
var pulseParkModes = map[park.Mode]bool{
	park.ModeDelay: true, park.ModeUntil: true, park.ModeForURL: true, park.ModeForCmd: true,
}

// pulseResumeOutcomes are the wake reasons the scheduler documents.
var pulseResumeOutcomes = map[string]bool{
	"elapsed": true, "matched": true, "timeout": true, "cancelled": true, "manual": true,
}

// pulseParkMode is the closed-enum form of a park mode.
func pulseParkMode(m park.Mode) string {
	if pulseParkModes[m] {
		return string(m)
	}
	return "other"
}

// pulseParked reports an agent parking: under the innermost node of ctx (the
// @park tool call) or the run in flight, with the job that will wake it.
func (a *AgentMode) pulseParked(ctx context.Context, req park.Request, jobID string, now time.Time) {
	if !pulse.Enabled() {
		return
	}
	parent := pulse.ParentFrom(ctx)
	if parent == "" {
		parent = a.orchRun.ID()
	}
	attrs := map[string]string{
		"mode":    pulseParkMode(req.Mode),
		"polling": strconv.FormatBool(req.IsPolling()),
	}
	if jobID != "" {
		attrs["job"] = scheduler.PulseJobNodePrefix + jobID
	}
	// When it is due: the first fire for a timer, the deadline for a poll.
	due := req.FireAt(now)
	if req.IsPolling() {
		due = req.Deadline
		if req.Interval > 0 {
			attrs["interval_s"] = strconv.FormatInt(int64(req.Interval.Seconds()), 10)
		}
	}
	if !due.IsZero() {
		attrs["eta_s"] = strconv.FormatInt(max(int64(due.Sub(now).Seconds()), 0), 10)
	}
	pulse.Point(pulse.KindBackground, pulseParkNode, parent, pulse.StatusOK, attrs)
}

// pulseResumeBegin opens the span of a resumed run. It hangs from the
// scheduler job that woke it when the snapshot names one, else from the
// innermost node of ctx; the returned ctx makes the resumed loop's requests
// and tools hang from it.
func (a *AgentMode) pulseResumeBegin(ctx context.Context, snap *park.Snapshot, outcome string) (context.Context, *pulse.Span) {
	if !pulse.Enabled() || snap == nil {
		return ctx, nil
	}
	if !pulseResumeOutcomes[outcome] {
		outcome = "other"
	}
	parent := pulse.ParentFrom(ctx)
	if snap.SchedulerJobID != "" {
		parent = scheduler.PulseJobNodePrefix + snap.SchedulerJobID
	}
	span := pulse.Begin(pulse.KindBackground, pulseResumeNode, parent).
		With("mode", pulseParkMode(snap.Park.Mode)).
		With("outcome", outcome)
	if !snap.CreatedAt.IsZero() {
		span = span.With("parked_s", strconv.FormatInt(max(int64(time.Since(snap.CreatedAt).Seconds()), 0), 10))
	}
	return span.Context(ctx), span
}

// pulseResumeEnd closes the span of a resumed run with how it ended. Parking
// again is the loop choosing to sleep, not a failure.
func pulseResumeEnd(span *pulse.Span, err error) {
	switch {
	case err == nil:
		span.Outcome(pulse.StatusOK, "completed")
	case errors.Is(err, errAgentParkedRequested):
		span.Outcome(pulse.StatusOK, "parked again")
	default:
		span.EndErr(err)
	}
}
