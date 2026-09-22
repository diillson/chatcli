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

	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// pulseRouteWatch turns the bus on and returns a reader of the session
// updates that carry a model: next blocks for one, quiet asserts that none
// arrives for a while. Subscribing happens before anything is emitted.
func pulseRouteWatch(t *testing.T) (next func() pulse.Event, quiet func()) {
	t.Helper()
	bus := pulse.Default()
	ch, cancel := bus.Subscribe(256)
	bus.SetEnabled(true)
	t.Cleanup(func() { bus.SetEnabled(false); cancel() })
	read := func(wait time.Duration) (pulse.Event, bool) {
		deadline := time.After(wait)
		for {
			select {
			case ev := <-ch:
				if ev.Kind == pulse.KindSession && ev.Attrs["model"] != "" {
					return ev, true
				}
			case <-deadline:
				return pulse.Event{}, false
			}
		}
	}
	next = func() pulse.Event {
		t.Helper()
		ev, ok := read(3 * time.Second)
		require.True(t, ok, "expected a session route update")
		return ev
	}
	quiet = func() {
		t.Helper()
		ev, ok := read(150 * time.Millisecond)
		require.False(t, ok, "unexpected route update: %+v", ev)
	}
	return next, quiet
}

func routeAttrs(ev pulse.Event) (provider, model, route, via string) {
	return ev.Attrs["provider"], ev.Attrs["model"], ev.Attrs["route"], ev.Attrs["via"]
}

// The bug this guards: after "@model use" the session card kept the boot
// model while every request hub lit up under the routed one.
func TestPulseRouteFollowsModelToolOverride(t *testing.T) {
	cliObj, _ := newRoutingTestCLI()
	plugins.SetModelRoutingAdapter(&modelRoutingAdapter{cli: cliObj})
	t.Cleanup(func() { plugins.SetModelRoutingAdapter(nil) })
	next, quiet := pulseRouteWatch(t)
	p := plugins.NewBuiltinModelPlugin()
	ctx := context.Background()

	_, err := p.Execute(ctx, []string{`{"cmd":"use","args":{"model":"GOOGLEAI:gemini-2.5-flash"}}`})
	require.NoError(t, err)
	ev := next()
	provider, model, route, via := routeAttrs(ev)
	assert.Equal(t, pulse.PhaseUpdate, ev.Phase)
	assert.Equal(t, pulseSessionNodeID, ev.ID)
	assert.Equal(t, "GOOGLEAI", provider)
	assert.Equal(t, "gemini-2.5-flash", model)
	assert.Equal(t, pulseRouteOverride, route)
	assert.Equal(t, "@model use", via)

	// The same handle again changes nothing and says nothing.
	_, err = p.Execute(ctx, []string{`{"cmd":"use","args":{"model":"GOOGLEAI:gemini-2.5-flash"}}`})
	require.NoError(t, err)
	quiet()

	_, err = p.Execute(ctx, []string{`{"cmd":"reset"}`})
	require.NoError(t, err)
	provider, model, route, via = routeAttrs(next())
	assert.Equal(t, "CLAUDEAI", provider)
	assert.Equal(t, "claude-sonnet-5", model)
	assert.Equal(t, pulseRouteSession, route)
	assert.Equal(t, "@model reset", via)
}

// A turn reports the pair it resolved — a skill hint included, which the
// session never sees — and a turn on the same pair stays silent.
func TestPulseRouteReportsWhatTheTurnResolved(t *testing.T) {
	cliObj, _ := newRoutingTestCLI()
	next, quiet := pulseRouteWatch(t)
	a := &AgentMode{cli: cliObj, logger: zap.NewNop()}
	ctx := context.Background()

	a.clientAndCtxForTurn(ctx)
	provider, model, route, via := routeAttrs(next())
	assert.Equal(t, "CLAUDEAI:claude-sonnet-5", provider+":"+model)
	assert.Equal(t, pulseRouteSession, route)
	assert.Equal(t, "agent turn", via)
	a.clientAndCtxForTurn(ctx)
	quiet()

	a.skillModelHint = "claude-haiku-4-5-20251001"
	a.clientAndCtxForTurn(ctx)
	provider, model, route, _ = routeAttrs(next())
	assert.Equal(t, "CLAUDEAI:claude-haiku-4-5-20251001", provider+":"+model)
	assert.Equal(t, pulseRouteSkill, route)

	// The override outranks the hint, and is reported as such.
	cliObj.setAgentRouteOverride("GOOGLEAI:gemini-2.5-flash", "@model use")
	_, _, route, via = routeAttrs(next())
	assert.Equal(t, pulseRouteOverride, route)
	assert.Equal(t, "@model use", via)
	a.clientAndCtxForTurn(ctx)
	quiet()

	// A chat turn with no hint keeps the session client: the source is the
	// session even though the caller passed the skill source.
	cliObj.pulseNoteResolvedRoute(cliObj.resolveSkillClient(""), pulseRouteSkill, "chat turn")
	provider, model, route, via = routeAttrs(next())
	assert.Equal(t, "CLAUDEAI:claude-sonnet-5", provider+":"+model)
	assert.Equal(t, pulseRouteSession, route)
	assert.Equal(t, "chat turn", via)
}

// A dashboard opened mid-task must see the routed pair, not the boot one,
// and the snapshot seeds the state so the next turn does not repeat it.
func TestPulseSessionSnapshotCarriesTheOverride(t *testing.T) {
	cliObj, _ := newRoutingTestCLI()
	cliObj.agentRouteOverride = "GOOGLEAI:gemini-2.5-flash" // set while the bus was off
	next, quiet := pulseRouteWatch(t)

	snap := cliObj.pulseSessionSnapshot()
	require.Len(t, snap, 1)
	provider, model, route, _ := routeAttrs(snap[0])
	assert.Equal(t, pulse.KindSession, snap[0].Kind)
	assert.Equal(t, pulse.PhaseStart, snap[0].Phase)
	assert.Equal(t, "chatcli", snap[0].Name)
	assert.Equal(t, "GOOGLEAI:gemini-2.5-flash", provider+":"+model)
	assert.Equal(t, pulseRouteOverride, route)

	a := &AgentMode{cli: cliObj, logger: zap.NewNop()}
	a.clientAndCtxForTurn(context.Background())
	quiet()

	cliObj.clearAgentRouteOverride("run start")
	_, model, route, via := routeAttrs(next())
	assert.Equal(t, "claude-sonnet-5", model)
	assert.Equal(t, pulseRouteSession, route)
	assert.Equal(t, "run start", via)
}

// The user's own switches report too, naming the command.
func TestPulseRouteFollowsSessionSwitches(t *testing.T) {
	cliObj, mgr := newRoutingTestCLI()
	next, quiet := pulseRouteWatch(t)
	ctx := context.Background()

	require.NoError(t, cliObj.ApplyOverrides(ctx, mgr, "GOOGLEAI", "gemini-2.5-flash"))
	provider, model, route, via := routeAttrs(next())
	assert.Equal(t, "GOOGLEAI:gemini-2.5-flash", provider+":"+model)
	assert.Equal(t, pulseRouteSession, route)
	assert.Equal(t, "rpc override", via)

	// Same pair: no client rebuilt, nothing reported.
	require.NoError(t, cliObj.ApplyOverrides(ctx, mgr, "GOOGLEAI", "gemini-2.5-flash"))
	quiet()

	require.NoError(t, cliObj.applyProviderSwitch(ctx, "CLAUDEAI"))
	provider, _, _, via = routeAttrs(next())
	assert.Equal(t, "CLAUDEAI", provider)
	assert.Equal(t, "/provider", via)
}

// Off costs nothing and records nothing: the state is seeded by the
// snapshot when recording starts, never by what happened while it was off.
func TestPulseRouteIsSilentWhileOff(t *testing.T) {
	cliObj, _ := newRoutingTestCLI()
	bus := pulse.Default()
	bus.SetEnabled(false)
	before := bus.Stats().Published

	cliObj.setAgentRouteOverride("GOOGLEAI:gemini-2.5-flash", "@model use")
	a := &AgentMode{cli: cliObj, logger: zap.NewNop()}
	a.clientAndCtxForTurn(context.Background())

	assert.Equal(t, before, bus.Stats().Published)
	assert.Empty(t, cliObj.pulseRoute.model, "nothing is remembered while off")
}
