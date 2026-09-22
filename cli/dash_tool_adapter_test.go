/*
 * ChatCLI - @dash tool adapter tests
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// newDashToolCLI builds a session whose recorder spools into a temp root.
// forced turns recording on from the start, as CHATCLI_DASH=1 does.
func newDashToolCLI(t *testing.T, forced bool) (*ChatCLI, *dashToolAdapter) {
	t.Helper()
	root := t.TempDir()
	ctl := pulse.Start(context.Background(), pulse.Options{Root: root, Forced: forced, Poll: 20 * time.Millisecond,
		Meta: pulse.Meta{PID: 4242, Surface: "test"}})
	require.NotNil(t, ctl)
	t.Cleanup(ctl.Close)
	if forced {
		require.Eventually(t, pulse.Default().Enabled, 3*time.Second, 10*time.Millisecond)
	}
	cliObj := &ChatCLI{pulse: ctl, logger: zap.NewNop(), unattended: true}
	return cliObj, &dashToolAdapter{cli: cliObj}
}

// waitSpooled blocks until the process's spool holds an event that matches.
func waitSpooled(t *testing.T, cliObj *ChatCLI, match func(pulse.Event) bool) {
	t.Helper()
	require.Eventually(t, func() bool {
		for _, ev := range readSpool(cliObj.pulse.Root(), pulse.Default().Instance()) {
			if match(ev) {
				return true
			}
		}
		return false
	}, 3*time.Second, 20*time.Millisecond)
}

func TestDashToolSaysWhenNothingIsRecording(t *testing.T) {
	_, a := newDashToolCLI(t, false)
	ctx := context.Background()

	out, err := a.Summary(ctx, false)
	require.NoError(t, err)
	assert.Contains(t, out, "not recording")
	out, err = a.Events(ctx, plugins.DashEventsQuery{})
	require.NoError(t, err)
	assert.Contains(t, out, "not recording")
	_, err = a.Mark(ctx, "phase: x")
	require.Error(t, err)
	out, err = a.Status(ctx)
	require.NoError(t, err)
	assert.Contains(t, out, "idle")
	assert.Contains(t, out, "not served")

	none := &dashToolAdapter{cli: &ChatCLI{}}
	_, err = none.Status(ctx)
	require.Error(t, err, "no recorder at all")
}

func TestDashToolReadsWhatTheDashboardSees(t *testing.T) {
	cliObj, a := newDashToolCLI(t, true)
	ctx := context.Background()

	pulse.Emit(pulse.Event{Kind: pulse.KindSession, Phase: pulse.PhaseStart, ID: pulseSessionNodeID, Name: "chatcli", Status: pulse.StatusRunning,
		Attrs: map[string]string{"provider": "CLAUDEAI", "model": "claude-sonnet-5", "route": "session"}})
	pulse.Emit(pulse.Event{Kind: pulse.KindAgent, Phase: pulse.PhaseStart, ID: "run-7", Parent: pulseSessionNodeID, Name: "coder", Status: pulse.StatusRunning,
		Attrs: map[string]string{"turn": "2", "max_turns": "30", "tool_calls": "3"}})
	pulse.Emit(pulse.Event{Kind: pulse.KindTool, Phase: pulse.PhaseStart, ID: "t1", Parent: "run-7", Name: "@shell", Status: pulse.StatusRunning})
	pulse.Emit(pulse.Event{Kind: pulse.KindTool, Phase: pulse.PhaseEnd, ID: "t1", Parent: "run-7", Name: "@shell", Status: pulse.StatusError, DurMS: 1500})
	pulse.Emit(pulse.Event{Kind: pulse.KindLLM, Phase: pulse.PhaseUpdate, ID: "usage:x", Parent: pulseSessionNodeID, Name: "CLAUDEAI:claude-haiku-4-5", Status: pulse.StatusOK,
		Attrs: map[string]string{"cost": "$0.03", "tokens": "8200"}})
	pulse.Emit(pulse.Event{Kind: pulse.KindSession, Phase: pulse.PhaseUpdate, ID: pulseSessionNodeID, Status: pulse.StatusRunning,
		Attrs: map[string]string{"provider": "CLAUDEAI", "model": "claude-haiku-4-5", "route": "override", "via": "@model use", "cost": "$0.42", "ctx": "37%"}})
	waitSpooled(t, cliObj, func(ev pulse.Event) bool { return ev.Attrs["via"] == "@model use" })

	out, err := a.Summary(ctx, false)
	require.NoError(t, err)
	assert.Contains(t, out, "test · pid 4242")
	assert.Contains(t, out, "session: chatcli · CLAUDEAI:claude-haiku-4-5 · route override via @model use · $0.42 · ctx 37%")
	assert.Contains(t, out, "agent: coder · run-7 · running · turn 2/30 · 3 tools")
	assert.Contains(t, out, "tool: @shell · 1× · ~1.5s · 1 err")
	assert.Contains(t, out, "llm: CLAUDEAI:claude-haiku-4-5")
	assert.Contains(t, out, "$0.03 · 8200 tokens")
	assert.Contains(t, out, "recent errors (1):")
	assert.Contains(t, out, "tool @shell end error 1.5s")

	// Every live process: this one is the only one, under its own header.
	all, err := a.Summary(ctx, true)
	require.NoError(t, err)
	assert.Contains(t, all, "pid 4242")

	out, err = a.Events(ctx, plugins.DashEventsQuery{Kind: "tool", Status: "error"})
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	require.Len(t, lines, 2, "header plus the one matching event: %s", out)
	assert.Contains(t, lines[0], "(tool error)")
	assert.Contains(t, lines[1], "tool @shell end error 1.5s")

	out, err = a.Events(ctx, plugins.DashEventsQuery{Limit: 2})
	require.NoError(t, err)
	lines = strings.Split(strings.TrimSpace(out), "\n")
	require.Len(t, lines, 3, "header plus the two newest: %s", out)
	assert.Contains(t, lines[2], "session", "newest last")
	assert.Contains(t, lines[2], "via=@model use")

	out, err = a.Events(ctx, plugins.DashEventsQuery{Kind: "rpc"})
	require.NoError(t, err)
	assert.Contains(t, out, "No events match")

	out, err = a.Mark(ctx, "  phase:\n running   the suite ")
	require.NoError(t, err)
	assert.Contains(t, out, "phase: running the suite")
	waitSpooled(t, cliObj, func(ev pulse.Event) bool {
		return ev.Kind == pulse.KindSession && ev.Phase == pulse.PhasePoint && ev.Attrs["note"] == "phase: running the suite"
	})
	_, err = a.Mark(ctx, "   ")
	require.Error(t, err)
}

func TestDashToolOpensAndStopsWithoutPrinting(t *testing.T) {
	cliObj, a := newDashToolCLI(t, true)
	ctx := context.Background()
	launched := 0
	prev := dashBrowserLauncher
	dashBrowserLauncher = func(string) error { launched++; return nil }
	t.Cleanup(func() { dashBrowserLauncher = prev })

	out := captureStdout(t, func() {
		got, err := a.Open(ctx, true)
		require.NoError(t, err)
		assert.Contains(t, got, "http://127.0.0.1:")
		assert.Contains(t, got, "Hand this address")
		assert.NotContains(t, got, "browser was opened", "unattended: no browser")
	})
	assert.Empty(t, out, "the tool never prints")
	assert.Equal(t, 0, launched)

	status, err := a.Status(ctx)
	require.NoError(t, err)
	assert.Contains(t, status, "http://127.0.0.1:")
	assert.Contains(t, status, "* test · pid 4242")

	// Second open reuses the server: same address.
	again, err := a.Open(ctx, false)
	require.NoError(t, err)
	url := strings.Fields(strings.SplitN(again, "\n", 2)[0])
	assert.Contains(t, status, url[len(url)-1])

	stopped, err := a.Off(ctx)
	require.NoError(t, err)
	assert.Contains(t, stopped, "stopped")
	assert.Nil(t, cliObj.dash.srv)
	stopped, err = a.Off(ctx)
	require.NoError(t, err)
	assert.Contains(t, stopped, "not running")
}

func TestDashCleanNoteAndClip(t *testing.T) {
	assert.Equal(t, "", dashCleanNote(" \n\t "))
	assert.Equal(t, "a b c", dashCleanNote("a\nb\tc"))
	long := strings.Repeat("x", 100)
	assert.Len(t, []rune(dashCleanNote(long)), dashMarkMaxRunes)
	assert.Equal(t, "héllo", dashClip("héllo", 10))
	assert.Equal(t, "hé…", dashClip("héllo", 3))
}

func TestRenderDashNodeShapes(t *testing.T) {
	agent := &pulse.Node{Kind: pulse.KindAgent, ID: "run-1", Name: "worker", Status: "ok", Ended: true, TotalMS: 12300, Attrs: map[string]string{"tool_calls": "4"}}
	assert.Equal(t, "worker · run-1 · ok · 4 tools · 12.3s", renderDashNode(agent))
	running := &pulse.Node{Kind: pulse.KindAgent, ID: "run-2", Name: "coder", Status: "running", Attrs: map[string]string{"action": "@read"}}
	assert.Equal(t, "coder · run-2 · running · @read", renderDashNode(running))
	hub := &pulse.Node{Kind: pulse.KindMCP, Name: "aws", Calls: 3, Active: 1, Timed: 2, TotalMS: 400, Errors: 1, Attrs: map[string]string{"state": "ready"}}
	assert.Equal(t, "aws · ready · 3× · 1 active · ~200ms · 1 err", renderDashNode(hub))
	session := &pulse.Node{Kind: pulse.KindSession, Name: "repl", Attrs: map[string]string{"model": "m", "route": "session", "requests": "9"}}
	assert.Equal(t, "repl · m · 9 requests", renderDashNode(session))
}
