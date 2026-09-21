/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package lessonq

import (
	"testing"
	"time"

	"github.com/diillson/chatcli/pkg/pulse"
)

// The hook only queues a lesson. What becomes of it — saved, nothing to
// learn, retried, lost to the dead-letter queue — happens here, in the
// background, and is the half of Reflexion nobody could see before.
func TestLessonJobOutcomeReachesTheDashboard(t *testing.T) {
	endLessonSpan(nil, OutcomeSuccess) // dashboard off: nil span, no-op

	bus := pulse.Default()
	ch, cancel := bus.Subscribe(32)
	bus.SetEnabled(true)
	t.Cleanup(func() { bus.SetEnabled(false); cancel() })

	cases := []struct {
		outcome ProcessOutcome
		status  string
		state   string
	}{
		{OutcomeSuccess, pulse.StatusOK, pulse.OutcomeLessonSaved},
		{OutcomeSkipped, pulse.StatusOK, pulse.OutcomeNoLesson},
		{OutcomeTransient, pulse.StatusError, pulse.OutcomeRetrying},
		{OutcomePermanent, pulse.StatusError, pulse.OutcomeDeadLetter},
	}
	for _, tc := range cases {
		endLessonSpan(pulse.BeginPattern(pulse.PatternReflexion, ""), tc.outcome)
	}
	var ends []pulse.Event
	deadline := time.After(3 * time.Second)
	for len(ends) < len(cases) {
		select {
		case ev := <-ch:
			if ev.Kind == pulse.KindPattern && ev.Phase == pulse.PhaseEnd {
				ends = append(ends, ev)
			}
		case <-deadline:
			t.Fatalf("got %d of %d end events", len(ends), len(cases))
		}
	}
	for i, tc := range cases {
		if ends[i].Status != tc.status || ends[i].Attrs["state"] != tc.state || ends[i].Name != pulse.PatternReflexion {
			t.Errorf("outcome %v = %+v, want %s/%s", tc.outcome, ends[i], tc.status, tc.state)
		}
	}
}
