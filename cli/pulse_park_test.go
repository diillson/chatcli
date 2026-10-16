/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/agent/park"
	"github.com/diillson/chatcli/cli/scheduler"
	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/diillson/chatcli/pkg/pulse/pulsetest"
	"go.uber.org/zap"
)

// Parking is a point under the @park call that names the mode, how long
// until it is due and the scheduler job that will wake it — never the note.
func TestPulseParkPointNamesModeEtaAndJob(t *testing.T) {
	t.Setenv("CHATCLI_PARK_DIR", t.TempDir())
	rec := pulsetest.Record(t)
	c := newCLIWithScheduler(t)
	c.logger = zap.NewNop()
	c.unattended = true
	a := NewAgentMode(c, zap.NewNop())

	ctx := pulse.WithParent(context.Background(), "tool-9")
	err := a.handleAgentPark(ctx, park.Request{Mode: park.ModeDelay, Delay: time.Hour, Note: "SECRET-NOTE"}, "", "")
	if !errors.Is(err, errAgentParkedRequested) {
		t.Fatalf("handleAgentPark: %v", err)
	}
	if a.inlineParkToken != "" {
		c.unregisterParkWaiter(a.inlineParkToken)
	}
	ev := rec.WaitFor(t, "park point", pulsetest.Is(pulse.KindBackground, pulseParkNode, pulse.PhasePoint))
	if ev.Parent != "tool-9" || ev.Status != pulse.StatusOK || ev.Attrs["mode"] != "delay" || ev.Attrs["polling"] != "false" {
		t.Fatalf("park point: %+v", ev)
	}
	if !strings.HasPrefix(ev.Attrs["job"], scheduler.PulseJobNodePrefix) || len(ev.Attrs["job"]) <= len(scheduler.PulseJobNodePrefix) {
		t.Fatalf("park point must name the waking job: %+v", ev.Attrs)
	}
	if eta, err := strconv.Atoi(ev.Attrs["eta_s"]); err != nil || eta < 3590 || eta > 3600 {
		t.Fatalf("eta_s: %+v", ev.Attrs)
	}
	for _, v := range ev.Attrs {
		if strings.Contains(v, "SECRET") {
			t.Fatalf("park note leaked: %+v", ev.Attrs)
		}
	}
}

// A polling park is due at its deadline and reports its poll interval.
func TestPulseParkPollingUsesDeadline(t *testing.T) {
	rec := pulsetest.Record(t)
	a := &AgentMode{}
	now := time.Now()
	a.pulseParked(context.Background(), park.Request{Mode: park.ModeForURL, URL: "https://secret.example/x",
		Interval: 30 * time.Second, Deadline: now.Add(10 * time.Minute)}, "", now)
	ev := rec.WaitFor(t, "park point", pulsetest.Is(pulse.KindBackground, pulseParkNode, pulse.PhasePoint))
	if ev.Attrs["mode"] != "for_url" || ev.Attrs["polling"] != "true" || ev.Attrs["eta_s"] != "600" || ev.Attrs["interval_s"] != "30" {
		t.Fatalf("polling park: %+v", ev.Attrs)
	}
	if _, ok := ev.Attrs["job"]; ok {
		t.Fatalf("no job, no job attr: %+v", ev.Attrs)
	}
}

// A resume hangs from the job that woke it, carries the wake reason as a
// closed enum and ends with how the resumed loop ended; its context makes
// the loop's own work hang from it.
func TestPulseResumeSpanHangsFromTheWakingJob(t *testing.T) {
	rec := pulsetest.Record(t)
	a := &AgentMode{}
	snap := &park.Snapshot{SchedulerJobID: "j-42", CreatedAt: time.Now().Add(-90 * time.Second), Park: park.Request{Mode: park.ModeUntil}}

	ctx, span := a.pulseResumeBegin(context.Background(), snap, "matched")
	if pulse.ParentFrom(ctx) != span.ID() || span.ID() == "" {
		t.Fatalf("resume ctx must carry the span: %q vs %q", pulse.ParentFrom(ctx), span.ID())
	}
	pulseResumeEnd(span, errAgentParkedRequested)
	ev := rec.WaitFor(t, "resume end", pulsetest.Is(pulse.KindBackground, pulseResumeNode, pulse.PhaseEnd))
	if ev.Parent != scheduler.PulseJobNodePrefix+"j-42" || ev.Status != pulse.StatusOK || ev.Attrs["state"] != "parked again" {
		t.Fatalf("resume span: %+v", ev)
	}
	if ev.Attrs["mode"] != "until" || ev.Attrs["outcome"] != "matched" {
		t.Fatalf("resume attrs: %+v", ev.Attrs)
	}
	if s, _ := strconv.Atoi(ev.Attrs["parked_s"]); s < 89 || s > 91 {
		t.Fatalf("parked_s: %+v", ev.Attrs)
	}

	_, span = a.pulseResumeBegin(pulse.WithParent(context.Background(), "turn-3"), &park.Snapshot{}, "free text from a payload")
	pulseResumeEnd(span, context.Canceled)
	manual := rec.WaitFor(t, "manual resume end", func(ev pulse.Event) bool {
		return pulsetest.Is(pulse.KindBackground, pulseResumeNode, pulse.PhaseEnd)(ev) && ev.Parent == "turn-3"
	})
	if ev := manual; ev.Status != pulse.StatusCancelled || ev.Attrs["outcome"] != "other" {
		t.Fatalf("manual resume: %+v", ev)
	}
}
