/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/agent/runs"
	"github.com/diillson/chatcli/pkg/persona"
	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pulseWatch turns the process-wide bus on for the test and returns a
// collector of the events of one kind.
func pulseWatch(t *testing.T, kind pulse.Kind) func(n int) []pulse.Event {
	t.Helper()
	bus := pulse.Default()
	ch, cancel := bus.Subscribe(256)
	bus.SetEnabled(true)
	t.Cleanup(func() { bus.SetEnabled(false); cancel() })
	var seen []pulse.Event
	return func(n int) []pulse.Event {
		t.Helper()
		deadline := time.After(3 * time.Second)
		for len(seen) < n {
			select {
			case ev := <-ch:
				if ev.Kind == kind {
					seen = append(seen, ev)
				}
			case <-deadline:
				t.Fatalf("got %d of %d %s events: %+v", len(seen), n, kind, seen)
			}
		}
		return seen
	}
}

// fakePlugin is the smallest plugins.Plugin.
type fakePlugin struct {
	name string
	err  error
}

func (p fakePlugin) Name() string        { return p.name }
func (p fakePlugin) Description() string { return "" }
func (p fakePlugin) Usage() string       { return "" }
func (p fakePlugin) Version() string     { return "" }
func (p fakePlugin) Path() string        { return "" }
func (p fakePlugin) Schema() string      { return "" }
func (p fakePlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}
func (p fakePlugin) ExecuteWithStream(_ context.Context, _ []string, onOutput func(string)) (string, error) {
	if onOutput != nil {
		onOutput("streamed SECRET-OUTPUT")
	}
	return "", p.err
}

func TestOrchestratorToolTapsCoverEveryExit(t *testing.T) {
	collect := pulseWatch(t, pulse.KindTool)
	_, run := runs.NewRegistry(4).Begin(context.Background(), runs.Info{Kind: runs.KindOrchestrator, Agent: "coder"})
	a := &AgentMode{orchRun: run} // no events sink: the taps must not depend on one

	ok := a.emitToolStart("@read", "read foo.go", `{"file":"SECRET-ARG"}`, []string{"read", "--file", "foo.go"})
	a.emitToolEnd(ok, "SECRET-OUTPUT", nil, "", time.Second)
	failed := a.emitToolStart("@coder", "exec", "{}", nil)
	a.emitToolEnd(failed, "", errors.New("SECRET-ERROR"), "E_FAIL", time.Second)
	a.emitBlockedTool("@http", "{}", "denied by policy")
	parked := a.emitToolStart("@park", "park", "{}", nil) // the park path never reaches emitToolEnd
	require.Len(t, a.pulseTools, 1)
	a.pulseCloseOpenTools()
	assert.Empty(t, a.pulseTools)
	a.emitToolEnd(parked, "", nil, "", 0) // late end after the sweep: ignored

	evs := collect(7)
	type row struct {
		name   string
		phase  pulse.Phase
		status string
	}
	want := []row{
		{"@read", pulse.PhaseStart, pulse.StatusRunning}, {"@read", pulse.PhaseEnd, pulse.StatusOK},
		{"@coder", pulse.PhaseStart, pulse.StatusRunning}, {"@coder", pulse.PhaseEnd, pulse.StatusError},
		{"@http", pulse.PhasePoint, pulse.StatusBlocked},
		{"@park", pulse.PhaseStart, pulse.StatusRunning}, {"@park", pulse.PhaseEnd, pulse.StatusCancelled},
	}
	for i, w := range want {
		assert.Equalf(t, w, row{evs[i].Name, evs[i].Phase, evs[i].Status}, "event %d", i)
		assert.Equalf(t, run.ID(), evs[i].Parent, "event %d hangs from the orchestrator run", i)
	}
	wantKind, _ := classifyToolCall("@read", []string{"read", "--file", "foo.go"})
	assert.Equal(t, string(wantKind), evs[1].Attrs["tool_kind"], "the kind is whatever the loop classifies the call as")
	wire, _ := json.Marshal(evs)
	for _, secret := range []string{"SECRET-ARG", "SECRET-OUTPUT", "SECRET-ERROR", "denied by policy", "foo.go"} {
		assert.NotContainsf(t, string(wire), secret, "%s must never reach the bus", secret)
	}
}

func TestExecBuiltinIsTappedAndRunBuiltinIsNot(t *testing.T) {
	collect := pulseWatch(t, pulse.KindTool)
	ctx, run := runs.NewRegistry(4).Begin(context.Background(), runs.Info{Kind: runs.KindHeadless})

	out, err := runBuiltin(ctx, fakePlugin{name: "@silent"}, nil)
	require.NoError(t, err)
	assert.Equal(t, "streamed SECRET-OUTPUT", out, "streamed output is still captured into the result")

	_, err = execBuiltin(ctx, fakePlugin{name: "@tree", err: errors.New("boom")}, nil)
	require.Error(t, err)

	evs := collect(2)
	assert.Equal(t, "@tree", evs[0].Name, "the worker-side runner reports nothing: its caller already does")
	assert.Equal(t, run.ID(), evs[0].Parent)
	assert.Equal(t, pulse.StatusError, evs[1].Status)
	wire, _ := json.Marshal(evs)
	assert.NotContains(t, string(wire), "SECRET-OUTPUT")
	assert.NotContains(t, string(wire), "boom")
}

func TestSkillTapsReportDeliveryOnceAndCollapse(t *testing.T) {
	collect := pulseWatch(t, pulse.KindSkill)
	_, run := runs.NewRegistry(4).Begin(context.Background(), runs.Info{Kind: runs.KindOrchestrator})
	a := &AgentMode{orchRun: run}

	goTesting := &persona.Skill{Name: "go-testing"}
	a.noteInjectedSkills(goTesting, nil)
	a.noteInjectedSkills(goTesting) // already delivered this run: not a new activation
	a.releaseCollapsedSkills([]string{"go-testing"}, 7)
	a.noteInjectedSkills(goTesting) // re-delivered after aging out
	pulseSkillsActivated("", pulseSkillSourceChat, "", "docs-style")

	evs := collect(4)
	assert.Equal(t, pulse.PhasePoint, evs[0].Phase)
	assert.Equal(t, "active", evs[0].Attrs["state"])
	assert.Equal(t, pulseSkillSourceStartup, evs[0].Attrs["source"])
	assert.Equal(t, run.ID(), evs[0].Parent)
	assert.Equal(t, pulse.PhaseUpdate, evs[1].Phase, "aging out is a state change, not an activation")
	assert.Equal(t, "collapsed", evs[1].Attrs["state"])
	assert.Equal(t, pulse.PhasePoint, evs[2].Phase)
	assert.Equal(t, "docs-style", evs[3].Name)
	assert.Equal(t, pulseSkillSourceChat, evs[3].Attrs["source"])
	assert.Empty(t, evs[3].Parent, "a chat turn has no run: the skill hangs from the session")

	assert.Equal(t, []string{"a", "b"}, skillNames([]*persona.Skill{{Name: "a"}, nil, {Name: "b"}}))
}

func TestTapsAreSilentWhileOff(t *testing.T) {
	before := pulse.Default().Stats().Published
	a := &AgentMode{}
	tc := a.emitToolStart("@read", "t", "{}", nil)
	a.emitToolEnd(tc, "", nil, "", 0)
	a.emitBlockedTool("@x", "{}", "no")
	a.pulseCloseOpenTools()
	a.noteInjectedSkills(&persona.Skill{Name: "s"})
	a.releaseCollapsedSkills([]string{"s"}, 1)
	_, _ = execBuiltin(context.Background(), fakePlugin{name: "@p"}, nil)
	assert.Nil(t, a.pulseTools, "off: not even the span table is allocated")
	assert.Equal(t, before, pulse.Default().Stats().Published)
}
