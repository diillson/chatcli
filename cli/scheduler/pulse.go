/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Live telemetry for the scheduler: the fifth sink of emit, next to the
 * audit log, the message bus, the hooks and the overlay bridge. The scheduler
 * is one background node on the dashboard. A job running is a timed span on
 * it; every other transition is a state change. The job name is the label the
 * user gave it in /schedule and the one /jobs already lists; the job payload,
 * its message and its execution output never leave the process.
 */
package scheduler

import (
	"github.com/diillson/chatcli/pkg/pulse"
)

// pulseSchedulerNode is the dashboard node every job reports on.
const pulseSchedulerNode = "scheduler"

// pulseEvent projects a scheduler transition onto the telemetry bus. ok is
// false for the transitions that are not worth a redraw.
func pulseEvent(evt Event) (pulse.Event, bool) {
	out := pulse.Event{
		Kind: pulse.KindBackground,
		ID:   "job:" + string(evt.JobID),
		Name: pulseSchedulerNode,
		TS:   evt.Timestamp,
	}
	switch evt.Type {
	case EventJobWaitTick:
		return pulse.Event{}, false // fires on every poll of a waiting job
	case EventJobRunning:
		out.Phase, out.Status = pulse.PhaseStart, pulse.StatusRunning
	case EventJobCompleted:
		out.Phase, out.Status = pulse.PhaseEnd, pulse.StatusOK
	case EventJobFailed, EventJobTimedOut:
		out.Phase, out.Status = pulse.PhaseEnd, pulse.StatusError
	case EventJobCancelled:
		out.Phase, out.Status = pulse.PhaseEnd, pulse.StatusCancelled
	case EventBreakerOpened:
		out.Phase, out.Status = pulse.PhaseUpdate, pulse.StatusError
	default:
		out.Phase, out.Status = pulse.PhaseUpdate, pulse.StatusOK
	}
	if evt.Execution != nil && out.Phase == pulse.PhaseEnd {
		out = out.Took(evt.Execution.Duration)
	}
	return out.With("state", string(evt.Type)).With("job", evt.Name), true
}

// pulseEmit is the telemetry sink of Scheduler.emit.
func pulseEmit(evt Event) {
	if !pulse.Enabled() {
		return
	}
	if out, ok := pulseEvent(evt); ok {
		pulse.Emit(out)
	}
}
