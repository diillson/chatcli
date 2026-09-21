/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package workers

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/agent/runs"
	"github.com/diillson/chatcli/pkg/pulse"
	"go.uber.org/zap"
)

func TestPulseToolNameAndStatus(t *testing.T) {
	if got := pulseToolName(resolvedToolCall{Subcmd: "read"}); got != "@coder" {
		t.Fatalf("engine subcommand hub = %q", got)
	}
	if got := pulseToolName(resolvedToolCall{pluginName: "@browser", Subcmd: "plugin"}); got != "@browser" {
		t.Fatalf("plugin hub = %q", got)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for name, tc := range map[string]struct {
		ctx  context.Context
		v    validatedTC
		res  execResult
		want string
	}{
		"ok":               {context.Background(), validatedTC{}, execResult{}, pulse.StatusOK},
		"failed":           {context.Background(), validatedTC{}, execResult{failed: true}, pulse.StatusError},
		"pre-blocked":      {context.Background(), validatedTC{blocked: true}, execResult{failed: true}, pulse.StatusBlocked},
		"policy-blocked":   {context.Background(), validatedTC{}, execResult{failed: true, record: ToolCallRecord{Error: errors.New(policyBlockedReason)}}, pulse.StatusBlocked},
		"cancelled":        {cancelled, validatedTC{}, execResult{failed: true}, pulse.StatusCancelled},
		"other tool error": {context.Background(), validatedTC{}, execResult{failed: true, record: ToolCallRecord{Error: errors.New("no such file")}}, pulse.StatusError},
	} {
		if got := pulseToolStatus(tc.ctx, tc.v, tc.res); got != tc.want {
			t.Errorf("%s: status = %q, want %q", name, got, tc.want)
		}
	}
}

// A worker tool call must show up under the worker's own run, which is the
// edge the dashboard draws, and must never carry its arguments.
func TestExecuteToolCallReportsASpanUnderItsRun(t *testing.T) {
	bus := pulse.Default()
	ch, cancelSub := bus.Subscribe(64)
	bus.SetEnabled(true)
	t.Cleanup(func() { bus.SetEnabled(false); cancelSub() })

	ctx, run := runs.NewRegistry(4).Begin(context.Background(), runs.Info{Kind: runs.KindWorker, Agent: "coder"})
	defer run.End(nil)

	v := validatedTC{blocked: true, msg: "refused", rtc: resolvedToolCall{ID: "t1", Subcmd: "exec", RawArgs: "--cmd SECRET-ARG"}}
	res := executeToolCall(ctx, v, nil, nil)
	if !res.failed || !strings.Contains(res.output, "refused") {
		t.Fatalf("the wrapper must return the inner result untouched: %+v", res)
	}

	var tool []pulse.Event
	deadline := time.After(3 * time.Second)
	for len(tool) < 2 {
		select {
		case ev := <-ch:
			if ev.Kind == pulse.KindTool {
				tool = append(tool, ev)
			}
		case <-deadline:
			t.Fatalf("got %d tool events", len(tool))
		}
	}
	if tool[0].Phase != pulse.PhaseStart || tool[0].Name != "@coder" || tool[0].Parent != run.ID() {
		t.Fatalf("start = %+v (run %s)", tool[0], run.ID())
	}
	if tool[1].Phase != pulse.PhaseEnd || tool[1].Status != pulse.StatusBlocked || tool[1].Attrs["subcmd"] != "exec" {
		t.Fatalf("end = %+v", tool[1])
	}
	wire, _ := json.Marshal(tool)
	if strings.Contains(string(wire), "SECRET-ARG") || strings.Contains(string(wire), "refused") {
		t.Fatalf("content leaked: %s", wire)
	}
}

func TestExecuteToolCallIsSilentWhileOff(t *testing.T) {
	before := pulse.Default().Stats().Published
	executeToolCall(context.Background(), validatedTC{blocked: true, msg: "x"}, nil, nil)
	if got := pulse.Default().Stats().Published; got != before {
		t.Fatalf("published %d events with the dashboard off", got-before)
	}
}

// Pattern #1: every worker ReAct loop is one span on the react node, under
// the worker's run, closing with how many turns it took.
func TestWorkerReActLoopIsReportedAsAPattern(t *testing.T) {
	bus := pulse.Default()
	ch, cancelSub := bus.Subscribe(128)
	bus.SetEnabled(true)
	t.Cleanup(func() { bus.SetEnabled(false); cancelSub() })

	ctx, run := runs.NewRegistry(4).Begin(context.Background(), runs.Info{Kind: runs.KindWorker, Agent: "search"})
	defer run.End(nil)
	client := &mockLLMClient{responses: []string{"SECRET-ANSWER final."}}
	if _, err := RunWorkerReAct(ctx, WorkerReActConfig{MaxTurns: 5, SystemPrompt: "test", ReadOnly: true}, "SECRET-TASK", client, nil, NewSkillSet(), nil, zap.NewNop()); err != nil {
		t.Fatal(err)
	}

	var react []pulse.Event
	deadline := time.After(3 * time.Second)
	for len(react) < 2 {
		select {
		case ev := <-ch:
			if ev.Kind == pulse.KindPattern && ev.Name == pulse.PatternReAct {
				react = append(react, ev)
			}
		case <-deadline:
			t.Fatalf("got %d react events", len(react))
		}
	}
	if react[0].Phase != pulse.PhaseStart || react[0].Parent != run.ID() {
		t.Fatalf("start = %+v", react[0])
	}
	if react[1].Phase != pulse.PhaseEnd || react[1].Status != pulse.StatusOK || !strings.HasSuffix(react[1].Attrs["state"], " turns") {
		t.Fatalf("end = %+v", react[1])
	}
	wire, _ := json.Marshal(react)
	if strings.Contains(string(wire), "SECRET") {
		t.Fatalf("task or answer leaked: %s", wire)
	}
}
