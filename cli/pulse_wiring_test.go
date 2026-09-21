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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/agent/runs"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func collectPulse(t *testing.T, bus *pulse.Bus, n int) []pulse.Event {
	t.Helper()
	ch, cancel := bus.Subscribe(64)
	t.Cleanup(cancel)
	out := make([]pulse.Event, 0, n)
	deadline := time.After(3 * time.Second)
	for len(out) < n {
		select {
		case ev := <-ch:
			out = append(out, ev)
		case <-deadline:
			t.Fatalf("got %d of %d pulse events: %+v", len(out), n, out)
		}
	}
	return out
}

func TestPulseRunEventMapsLifecycleAndParent(t *testing.T) {
	started := time.Now().Add(-3 * time.Second)

	begin := pulseRunEvent(runs.Info{ID: "run-1", Kind: runs.KindOrchestrator, Agent: "coder", Status: runs.StatusRunning, Origin: "repl", StartedAt: started})
	assert.Equal(t, pulse.KindAgent, begin.Kind)
	assert.Equal(t, pulse.PhaseStart, begin.Phase)
	assert.Equal(t, pulseSessionNodeID, begin.Parent, "a root run hangs from the session node")
	assert.Equal(t, pulse.StatusRunning, begin.Status)
	assert.Equal(t, "orchestrator", begin.Attrs["run_kind"])

	progress := pulseRunEvent(runs.Info{ID: "run-2", ParentID: "run-1", Kind: runs.KindWorker, Agent: "reviewer", Status: runs.StatusRunning,
		Turn: 2, MaxTurns: 30, Action: "read cli/foo.go", ToolCalls: 4, CallID: "c7", StartedAt: started})
	assert.Equal(t, pulse.PhaseUpdate, progress.Phase)
	assert.Equal(t, "run-1", progress.Parent, "a worker keeps its real parent: that link IS the graph edge")
	assert.Equal(t, "2", progress.Attrs["turn"])
	assert.Equal(t, "30", progress.Attrs["max_turns"])
	assert.Equal(t, "4", progress.Attrs["tool_calls"])
	assert.Equal(t, "c7", progress.Attrs["call_id"])

	for status, want := range map[runs.Status]string{
		runs.StatusCompleted: pulse.StatusOK,
		runs.StatusFailed:    pulse.StatusError,
		runs.StatusCancelled: pulse.StatusCancelled,
	} {
		end := pulseRunEvent(runs.Info{ID: "run-3", Status: status, StartedAt: started, EndedAt: started.Add(1500 * time.Millisecond)})
		assert.Equal(t, pulse.PhaseEnd, end.Phase)
		assert.Equal(t, want, end.Status)
		assert.EqualValues(t, 1500, end.DurMS)
	}
}

// The task given to an agent is prompt content and the error is free-form
// text: neither may reach the bus, whatever else changes in the projection.
func TestPulseRunEventNeverCarriesContent(t *testing.T) {
	const secretTask = "refactor the billing module using key sk-live-TASKSECRET"
	const secretErr = "provider said: invalid key sk-live-ERRSECRET"
	ev := pulseRunEvent(runs.Info{ID: "run-9", Agent: "coder", Task: secretTask, Err: secretErr, Status: runs.StatusFailed,
		StartedAt: time.Now().Add(-time.Second), EndedAt: time.Now()})
	wire, err := json.Marshal(ev)
	require.NoError(t, err)
	assert.NotContains(t, string(wire), "TASKSECRET")
	assert.NotContains(t, string(wire), "ERRSECRET")
}

func TestPulseLLMTapPairsRequestsPerModel(t *testing.T) {
	bus := pulse.New("t")
	bus.SetEnabled(true)
	tap := newPulseLLMTap(bus)
	now := time.Now()

	go func() {
		tap.observe(client.RequestAuditEvent{Time: now, Phase: "send", Provider: "openai", Model: "gpt-x", Fields: map[string]string{"payload_bytes": "512", "prompt": "must-not-pass"}})
		tap.observe(client.RequestAuditEvent{Time: now, Phase: "send", Provider: "claudeai", Model: "fable"})
		tap.observe(client.RequestAuditEvent{Time: now, Phase: "recv", Provider: "claudeai", Model: "fable", Status: "error", Duration: 2 * time.Second})
		tap.observe(client.RequestAuditEvent{Time: now, Phase: "recv", Provider: "openai", Model: "gpt-x", Status: "success", Duration: time.Second, Fields: map[string]string{"output_tokens": "42"}})
		tap.observe(client.RequestAuditEvent{Time: now, Phase: "recv", Provider: "openai", Model: "gpt-x", Status: "success"}) // orphan: dropped
		tap.observe(client.RequestAuditEvent{Time: now, Phase: "send", Provider: "xai", Model: "grok"})
		tap.observe(client.RequestAuditEvent{Time: now, Phase: "recv", Provider: "xai", Model: "grok", Status: "canceled"})
	}()
	got := collectPulse(t, bus, 6)

	assert.Equal(t, "llm-1", got[0].ID)
	assert.Equal(t, pulse.PhaseStart, got[0].Phase)
	assert.Equal(t, "openai:gpt-x", got[0].Name)
	assert.Equal(t, "512", got[0].Attrs["payload_bytes"])
	assert.NotContains(t, got[0].Attrs, "prompt", "only the allow-listed numeric fields pass")

	assert.Equal(t, "llm-2", got[2].ID, "the claudeai response closes the claudeai node")
	assert.Equal(t, pulse.StatusError, got[2].Status)
	assert.EqualValues(t, 2000, got[2].DurMS)

	assert.Equal(t, "llm-1", got[3].ID)
	assert.Equal(t, pulse.StatusOK, got[3].Status)
	assert.Equal(t, "42", got[3].Attrs["output_tokens"])

	assert.Equal(t, "llm-3", got[4].ID, "the orphan response emitted nothing")
	assert.Equal(t, pulse.StatusCancelled, got[5].Status)
	assert.Empty(t, tap.open, "every matched request leaves the open table")
}

func TestPulseLLMTapIsSilentWhileOff(t *testing.T) {
	bus := pulse.New("t")
	tap := newPulseLLMTap(bus)
	tap.observe(client.RequestAuditEvent{Phase: "send", Provider: "openai", Model: "gpt-x"})
	assert.Empty(t, tap.open, "off: not even the pairing table grows")
}

func TestPulseForcedEnv(t *testing.T) {
	for v, want := range map[string]bool{"1": true, "true": true, " ON ": true, "yes": true, "0": false, "": false, "off": false, "maybe": false} {
		t.Setenv(pulseDashEnv, v)
		assert.Equalf(t, want, pulseForced(), "CHATCLI_DASH=%q", v)
	}
}

// The regression this whole design exists to prevent: attaching the live
// telemetry must not evict the hub bridge from the run registry nor the
// audit log from the request auditor, and detaching must not take them down.
func TestInitPulseLeavesDefaultObserversInPlace(t *testing.T) {
	t.Setenv(pulseDashEnv, "1")
	bridge, auditLog := 0, 0
	runs.Default().OnEvent(func(runs.Info) { bridge++ })
	client.RegisterRequestAuditor(func(client.RequestAuditEvent) { auditLog++ })
	t.Cleanup(func() { runs.Default().OnEvent(nil); client.RegisterRequestAuditor(nil) })

	// The bus is process-wide: another test may already have spooled under
	// this instance, so only what comes after this cursor belongs here.
	defaultRoot, err := pulse.DefaultRoot()
	require.NoError(t, err)
	_, base, _ := pulse.ReadSince(defaultRoot, pulse.Default().Instance(), 0, 0)

	c := &ChatCLI{logger: zap.NewNop(), Provider: "OPENAI", Model: "gpt-x"}
	c.initPulse(context.Background(), "repl")
	require.NotNil(t, c.pulse)
	first := c.pulse
	c.initPulse(context.Background(), "repl")
	assert.Same(t, first, c.pulse, "a second init is a no-op")
	c.SetAuditSurface("acp")

	require.Eventually(t, c.pulse.Recording, 3*time.Second, 10*time.Millisecond)
	root, instance := c.pulse.Root(), pulse.Default().Instance()

	_, run := runs.Default().Begin(context.Background(), runs.Info{Kind: runs.KindWorker, Agent: "coder", Task: "secret task text"})
	run.End(errors.New("boom"))
	client.LogRequestStart(nil, "openai", "gpt-x")
	client.LogRequestFinish(nil, "openai", "gpt-x", "success", time.Second)
	assert.Equal(t, 2, bridge, "hub bridge slot still receives begin + end")
	assert.Equal(t, 2, auditLog, "audit log slot still receives send + recv")

	var spooled []pulse.Event
	require.Eventually(t, func() bool {
		spooled, _, _ = pulse.ReadSince(root, instance, base, 0)
		kinds := map[pulse.Kind]int{}
		for _, ev := range spooled {
			kinds[ev.Kind]++
		}
		return kinds[pulse.KindSession] >= 1 && kinds[pulse.KindAgent] >= 2 && kinds[pulse.KindLLM] >= 2
	}, 3*time.Second, 20*time.Millisecond, "session snapshot, run and request must reach the spool")
	// Looked up, not indexed: the bus is process-wide, so an event queued by
	// an earlier consumer just before it was disabled may still be delivered
	// ahead of this snapshot. The page does not depend on the order either.
	var session *pulse.Event
	for i := range spooled {
		if spooled[i].Kind == pulse.KindSession && spooled[i].Attrs["provider"] == "OPENAI" {
			session = &spooled[i]
			break
		}
	}
	require.NotNil(t, session, "the session node must be replayed when recording starts")
	assert.Equal(t, "gpt-x", session.Attrs["model"])
	assert.Equal(t, "true", session.Attrs["snapshot"])
	wire, _ := json.Marshal(spooled)
	assert.NotContains(t, string(wire), "secret task text")

	metas := pulse.ListInstances(root)
	require.NotEmpty(t, metas)
	assert.Equal(t, "acp", metas[0].Surface, "SetAuditSurface renames the pulse surface too")

	c.shutdownPulse()
	c.shutdownPulse() // idempotent
	assert.Nil(t, c.pulse)
	assert.False(t, pulse.Default().Enabled())

	_, run2 := runs.Default().Begin(context.Background(), runs.Info{Kind: runs.KindWorker})
	run2.End(nil)
	client.LogRequestStart(nil, "openai", "gpt-x")
	assert.Equal(t, 4, bridge, "after pulse detaches the bridge keeps observing")
	assert.Equal(t, 3, auditLog, "after pulse detaches the audit log keeps observing")
}

func TestPulseNilReceiversAreSafe(t *testing.T) {
	var c *ChatCLI
	c.initPulse(context.Background(), "repl")
	c.pulseSurface("x")
	c.shutdownPulse()
	(&ChatCLI{}).pulseSurface("x") // no controller yet
}

func TestShowConfigPulse(t *testing.T) {
	t.Setenv(pulseDashEnv, "")
	out := captureStdout(t, func() { (&ChatCLI{}).showConfigPulse() })
	assert.Contains(t, out, pulseDashEnv)
	assert.Contains(t, out, "idle")
	assert.Contains(t, out, "never prompt or tool content")

	routed := captureStdout(t, func() { (&ChatCLI{}).routeConfigCommand(context.Background(), []string{"dash"}) })
	assert.Contains(t, routed, pulseDashEnv, "/config dash routes to the section")
	assert.Contains(t, configSectionNames, "dash")
}

func TestStoragePulseSpoolRetention(t *testing.T) {
	now := time.Now()
	root := t.TempDir()
	dir := filepath.Join(root, StorePulse)
	writeMeta := func(name string, ended bool, age time.Duration) {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, name), 0o750))
		data, _ := json.Marshal(pulse.Meta{Instance: name, Ended: ended, Heartbeat: now.Add(-age)})
		require.NoError(t, os.WriteFile(filepath.Join(dir, name, "meta.json"), data, 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(dir, name, "events-00000000000000000001.ndjson"), []byte(strings.Repeat("x", 100)), 0o600))
	}
	writeMeta("old-dead", true, 48*time.Hour)
	writeMeta("recent-dead", true, time.Hour)
	writeMeta("live", false, 0)

	res, err := RunStorage(context.Background(), StorageOptions{Root: root, Now: now})
	require.NoError(t, err)
	st := storeByName(res, StorePulse)
	assert.Equal(t, PolicyRuns, st.Policy)
	assert.False(t, st.Protected)
	assert.Equal(t, 1, st.Prunable, "only the dead process past retention")
	assert.True(t, IsStorageStore(StorePulse))

	applied, err := RunStorage(context.Background(), StorageOptions{Root: root, Now: now, Apply: true, Only: StorePulse})
	require.NoError(t, err)
	assert.True(t, applied.Applied)
	assert.NoDirExists(t, filepath.Join(dir, "old-dead"))
	assert.DirExists(t, filepath.Join(dir, "recent-dead"))
	assert.DirExists(t, filepath.Join(dir, "live"))
}
