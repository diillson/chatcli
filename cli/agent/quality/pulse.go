/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Live telemetry for the quality pipeline. Self-Refine, CoVe and Reflexion
 * run silently: they rewrite a worker's output, flag a discrepancy or queue a
 * lesson without printing a line. The dashboard is where that becomes
 * visible, so each reports not only that it fired but what it concluded.
 *
 * The taps sit inside each hook, after its guards. The pipeline wrapper runs
 * every hook on every dispatch, including the ones that are disabled or do
 * not apply; tapping there would show a pattern firing when it did nothing.
 */
package quality

import (
	"context"
	"strconv"

	"github.com/diillson/chatcli/cli/agent/runs"
	"github.com/diillson/chatcli/pkg/pulse"
)

// Outcome labels. Fixed strings, never model output.
const (
	outcomeRewrote     = "rewrote draft"
	outcomeUnchanged   = "kept draft"
	outcomeRolledBack  = "rolled back"
	outcomeFailed      = "failed"
	outcomeClean       = "verified clean"
	outcomeDiscrepancy = "found discrepancy"
	outcomeCorrected   = "corrected draft"
	outcomeQueued      = "lesson queued"
	outcomeQueueFailed = "queue failed"
)

// beginPattern opens a pattern span under the run the hook is serving.
func beginPattern(ctx context.Context, name string) *pulse.Span {
	if !pulse.Enabled() {
		return nil
	}
	return pulse.BeginPattern(name, runs.FromContext(ctx).ID())
}

// refineOutcome names what a Self-Refine run concluded.
func refineOutcome(failed, rolledBack, changed bool) (status, outcome string) {
	switch {
	case failed && !changed:
		return pulse.StatusError, outcomeFailed
	case rolledBack:
		return pulse.StatusOK, outcomeRolledBack
	case changed:
		return pulse.StatusOK, outcomeRewrote
	default:
		return pulse.StatusOK, outcomeUnchanged
	}
}

// endRefine closes a Self-Refine span.
func endRefine(span *pulse.Span, passes int, failed, rolledBack, changed bool) {
	if span == nil {
		return
	}
	status, outcome := refineOutcome(failed, rolledBack, changed)
	span.With("passes", strconv.Itoa(passes)).Outcome(status, outcome)
}

// reflexionTriggered reports that a failure, a hallucination or a manual
// request made Reflexion queue a lesson. What becomes of it is reported by
// the background worker that distills it.
func reflexionTriggered(ctx context.Context, trigger, status, outcome string) {
	if !pulse.Enabled() {
		return
	}
	pulse.Point(pulse.KindPattern, pulse.PatternReflexion, runs.FromContext(ctx).ID(), status,
		map[string]string{"state": outcome, "trigger": trigger})
}

// reasoningApplied reports the effort tier auto-attached to an agent.
func reasoningApplied(ctx context.Context, agent, effort string) {
	if !pulse.Enabled() {
		return
	}
	pulse.Point(pulse.KindPattern, pulse.PatternReasoning, runs.FromContext(ctx).ID(), pulse.StatusOK,
		map[string]string{"state": "effort " + effort, "agent": agent})
}
