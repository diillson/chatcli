/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"sync"
	"testing"

	"github.com/diillson/chatcli/cli/agent/runs"
	"github.com/diillson/chatcli/pkg/pulse"
	"go.uber.org/zap"
)

// A headless run fired by a scheduler job names that job as its call ID, so
// the agent node on the dashboard links back to the job node.
func TestPulseHeadlessRunLinksToItsJob(t *testing.T) {
	var mu sync.Mutex
	var seen []runs.Info
	runs.Default().OnEventKeyed("pulse-headless-test", func(info runs.Info) {
		if info.Agent == "scheduler" {
			mu.Lock()
			seen = append(seen, info)
			mu.Unlock()
		}
	})
	t.Cleanup(func() { runs.Default().OnEventKeyed("pulse-headless-test", nil) })

	c := &ChatCLI{logger: zap.NewNop(), Client: &scriptedClient{name: "m", response: "all done"}}
	b := newSchedulerBridge(c)
	ctx := pulse.WithParent(context.Background(), "job:j-7")
	if _, err := b.runHeadlessAgent(ctx, "do the thing", "", "nightly", false, nil); err != nil {
		t.Fatalf("runHeadlessAgent: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 {
		t.Fatal("headless run never registered")
	}
	for _, info := range seen {
		if info.CallID != "job:j-7" || info.PulseParent != "job:j-7" {
			t.Fatalf("headless run must carry its job: %+v", info)
		}
	}
	if ev := pulseRunEvent(seen[len(seen)-1]); ev.Attrs["call_id"] != "job:j-7" || ev.Attrs["origin"] != "scheduler" {
		t.Fatalf("agent node: %+v", ev)
	}
}

func TestSchedulerJobNodeFrom(t *testing.T) {
	if got := schedulerJobNodeFrom(pulse.WithParent(context.Background(), "job:x")); got != "job:x" {
		t.Fatalf("job ctx: %q", got)
	}
	if got := schedulerJobNodeFrom(pulse.WithParent(context.Background(), "tool-3")); got != "" {
		t.Fatalf("non-job ctx: %q", got)
	}
}
