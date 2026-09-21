/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/agent/runs"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// Two workers on the same model, responses arriving out of order. Oldest
// first per model would close the wrong node and hang both requests from the
// session; with the caller each response closes its own request, under the
// agent that made it.
func TestLLMRequestsHangFromTheAgentThatMadeThem(t *testing.T) {
	bus := pulse.New("t")
	bus.SetEnabled(true)
	tap := newPulseLLMTap(bus)
	collect := subscribePulse(t, bus)
	now := time.Now()
	send := func(caller string) {
		tap.observe(client.RequestAuditEvent{Time: now, Phase: "send", Provider: "claudeai", Model: "fable", Fields: map[string]string{client.CallerFieldKey: caller}})
	}
	recv := func(caller, status string) {
		tap.observe(client.RequestAuditEvent{Time: now, Phase: "recv", Provider: "claudeai", Model: "fable", Status: status, Duration: time.Second, Fields: map[string]string{client.CallerFieldKey: caller}})
	}

	send("run-reviewer") // llm-1
	send("run-coder")    // llm-2
	send("")             // llm-3: a chat turn, nobody in particular
	recv("run-coder", "success")
	recv("", "success")
	recv("run-reviewer", "error")

	evs := collect(6)
	byID := map[string][]pulse.Event{}
	for _, ev := range evs {
		byID[ev.ID] = append(byID[ev.ID], ev)
	}
	for id, want := range map[string]struct{ parent, status string }{
		"llm-1": {"run-reviewer", pulse.StatusError},
		"llm-2": {"run-coder", pulse.StatusOK},
		"llm-3": {pulseSessionNodeID, pulse.StatusOK},
	} {
		pair := byID[id]
		require.Lenf(t, pair, 2, "%s must have exactly a start and an end", id)
		assert.Equalf(t, want.parent, pair[0].Parent, "%s start parent", id)
		assert.Equalf(t, want.parent, pair[1].Parent, "%s end hangs from the same node as its start", id)
		assert.Equalf(t, want.status, pair[1].Status, "%s outcome", id)
		assert.Equal(t, "CLAUDEAI:fable", pair[0].Name, "the hub is still the model, not the caller")
	}
	assert.Empty(t, tap.open)
}

// End to end through the real chokepoint: the resolver initPulse installs
// reads the run from the request context.
func TestCallerResolverReadsTheRunFromTheRequestContext(t *testing.T) {
	t.Setenv(pulseDashEnv, "")
	c := &ChatCLI{logger: zap.NewNop()}
	c.initPulse(context.Background(), "repl")
	t.Cleanup(func() { c.shutdownPulse(); client.SetCallerResolver(nil) })

	ctx, run := runs.NewRegistry(4).Begin(context.Background(), runs.Info{Kind: runs.KindWorker, Agent: "coder"})
	defer run.End(nil)
	assert.Equal(t, run.ID(), client.CallerField(ctx).String)
	assert.Empty(t, client.CallerField(context.Background()).String, "no run in the context: no caller")
}
