/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package scheduler

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/pkg/pulse"
)

func TestSchedulerTransitionsMapOntoTheDashboard(t *testing.T) {
	ts := time.Now()
	base := Event{JobID: "j-1", Name: "nightly-report", Timestamp: ts, Message: "SECRET-MESSAGE",
		Data: map[string]any{"prompt": "SECRET-PAYLOAD"}}
	for _, tc := range []struct {
		typ    EventType
		phase  pulse.Phase
		status string
	}{
		{EventJobRunning, pulse.PhaseStart, pulse.StatusRunning},
		{EventJobCompleted, pulse.PhaseEnd, pulse.StatusOK},
		{EventJobFailed, pulse.PhaseEnd, pulse.StatusError},
		{EventJobTimedOut, pulse.PhaseEnd, pulse.StatusError},
		{EventJobCancelled, pulse.PhaseEnd, pulse.StatusCancelled},
		{EventBreakerOpened, pulse.PhaseUpdate, pulse.StatusError},
		{EventJobScheduled, pulse.PhaseUpdate, pulse.StatusOK},
		{EventDaemonStarted, pulse.PhaseUpdate, pulse.StatusOK},
	} {
		evt := base
		evt.Type = tc.typ
		evt.Execution = &ExecutionResult{Duration: 1500 * time.Millisecond, Output: "SECRET-OUTPUT"}
		got, ok := pulseEvent(evt)
		if !ok {
			t.Fatalf("%s must be reported", tc.typ)
		}
		if got.Kind != pulse.KindBackground || got.Name != pulseSchedulerNode || got.ID != "job:j-1" || !got.TS.Equal(ts) {
			t.Fatalf("%s = %+v", tc.typ, got)
		}
		if got.Phase != tc.phase || got.Status != tc.status {
			t.Errorf("%s = %s/%s, want %s/%s", tc.typ, got.Phase, got.Status, tc.phase, tc.status)
		}
		if got.Attrs["state"] != string(tc.typ) || got.Attrs["job"] != "nightly-report" {
			t.Errorf("%s attrs = %+v", tc.typ, got.Attrs)
		}
		if wantDur := tc.phase == pulse.PhaseEnd; (got.DurMS == 1500) != wantDur {
			t.Errorf("%s duration = %d", tc.typ, got.DurMS)
		}
		wire, _ := json.Marshal(got)
		if strings.Contains(string(wire), "SECRET") {
			t.Fatalf("%s leaked job content: %s", tc.typ, wire)
		}
	}
	if _, ok := pulseEvent(Event{Type: EventJobWaitTick}); ok {
		t.Fatal("the wait tick fires on every poll and must not be reported")
	}
}

func TestSchedulerSinkFollowsTheBus(t *testing.T) {
	before := pulse.Default().Stats().Published
	pulseEmit(Event{Type: EventJobRunning, JobID: "j"})
	if pulse.Default().Stats().Published != before {
		t.Fatal("dashboard off: nothing may be published")
	}

	bus := pulse.Default()
	ch, cancel := bus.Subscribe(16)
	bus.SetEnabled(true)
	t.Cleanup(func() { bus.SetEnabled(false); cancel() })
	pulseEmit(Event{Type: EventJobWaitTick, JobID: "j"})
	pulseEmit(Event{Type: EventJobRunning, JobID: "j", Name: "probe"})
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Kind != pulse.KindBackground {
				continue
			}
			if ev.Attrs["state"] != string(EventJobRunning) {
				t.Fatalf("first scheduler event = %+v, want job.running (the tick is skipped)", ev)
			}
			return
		case <-deadline:
			t.Fatal("job.running never reached the bus")
		}
	}
}
