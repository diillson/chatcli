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
	"strconv"
	"time"

	"github.com/diillson/chatcli/pkg/pulse"
)

// pulseSchedulerNode is the dashboard node every job reports on.
const pulseSchedulerNode = "scheduler"

// nextFireDataKey is the event data key carrying when a job fires next, on
// creation and on every recurring re-arm.
const nextFireDataKey = "next_fire_at"

// pulseJobNodeID is the bus ID of a job's node. The action of a firing job
// runs under it, so whatever the job starts names it as its parent.
func pulseJobNodeID(id JobID) string {
	return PulseJobNodePrefix + string(id)
}

// PulseJobNodePrefix is how a job's node ID on the live dashboard starts. A
// run started by a job carries that node ID as its correlation handle.
const PulseJobNodePrefix = "job:"

// pulseEvent projects a scheduler transition onto the telemetry bus. ok is
// false for the transitions that are not worth a redraw.
func pulseEvent(evt Event) (pulse.Event, bool) {
	out := pulse.Event{
		Kind: pulse.KindBackground,
		ID:   pulseJobNodeID(evt.JobID),
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
	out = out.With("state", string(evt.Type)).With("job", evt.Name).With("owner", string(evt.Owner.Kind))
	// Fixed enums only: the action type and the schedule kind, never the
	// payload; when the job fires next, as seconds from the transition.
	action := pulseDataString(evt.Data, "action_type")
	if action == "" {
		action = pulseDataString(evt.Data, "action")
	}
	out = out.With("action", action)
	out = out.With("schedule", pulseDataString(evt.Data, "schedule"))
	if next := pulseDataTime(evt.Data, nextFireDataKey); !next.IsZero() {
		secs := int64(next.Sub(evt.Timestamp).Seconds())
		if secs < 0 {
			secs = 0
		}
		out = out.With("next_fire_in_s", strconv.FormatInt(secs, 10))
	}
	return out, true
}

// pulseDataString reads a string value of the event data, "" otherwise.
func pulseDataString(data map[string]any, key string) string {
	s, _ := data[key].(string)
	return s
}

// pulseDataTime reads a time value of the event data. Events relayed over
// IPC arrive JSON-decoded, so an RFC 3339 string is accepted too.
func pulseDataTime(data map[string]any, key string) time.Time {
	switch v := data[key].(type) {
	case time.Time:
		return v
	case string:
		t, _ := time.Parse(time.RFC3339Nano, v)
		return t
	}
	return time.Time{}
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
