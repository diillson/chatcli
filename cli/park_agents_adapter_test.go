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

	"github.com/diillson/chatcli/cli/agent/park"
	"github.com/diillson/chatcli/cli/agent/runs"
	"github.com/diillson/chatcli/cli/plugins"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestLiveAgentsAdapter_WaitAndTranscript(t *testing.T) {
	reg := runs.NewRegistry(10)
	adapter := newLiveAgentsAdapter(reg)
	var _ plugins.AgentsWatcher = adapter

	_, err := adapter.Wait(context.Background(), "run-nope", time.Second)
	require.Error(t, err)

	_, run := reg.Begin(context.Background(), runs.Info{Kind: runs.KindWorker, Agent: "coder", Task: "do it"})
	id := run.ID()
	run.SetTurn(1, 5)
	run.SetAction("read main.go")

	// Timeout returns the current state, not an error.
	out, err := adapter.Wait(context.Background(), id, 300*time.Millisecond)
	require.NoError(t, err)
	assert.Contains(t, out, "WAIT TIMED OUT")
	assert.Contains(t, out, "status=running")

	// ctx cancellation ends the wait with an error.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = adapter.Wait(ctx, id, time.Minute)
	require.ErrorIs(t, err, context.Canceled)

	// A run that finishes mid-wait returns its final detail.
	go func() {
		time.Sleep(400 * time.Millisecond)
		run.SetAction("write main_test.go")
		run.Note("output: all green")
		run.End(nil)
	}()
	out, err = adapter.Wait(context.Background(), id, 10*time.Second)
	require.NoError(t, err)
	assert.Contains(t, out, "RUN "+id+" FINISHED status=completed")
	assert.Contains(t, out, "task=do it")

	// Transcript: whole log, then a tail.
	out, err = adapter.Transcript(id, 200)
	require.NoError(t, err)
	assert.Contains(t, out, "ACTIVITY LOG "+id+" status=completed (5 of 5 entries")
	assert.Contains(t, out, "turn 1/5")
	assert.Contains(t, out, "action: read main.go")
	assert.Contains(t, out, "output: all green")
	assert.Contains(t, out, "end status=completed")

	out, err = adapter.Transcript(id, 2)
	require.NoError(t, err)
	assert.Contains(t, out, "(2 of 5 entries")
	assert.NotContains(t, out, "turn 1/5")
	assert.Contains(t, out, "end status=completed")

	_, err = adapter.Transcript("run-nope", 10)
	require.Error(t, err)
}

func TestLiveParkAdapter_CancelParkWakesTheAgent(t *testing.T) {
	t.Setenv("CHATCLI_PARK_DIR", t.TempDir())
	cli := &ChatCLI{logger: zap.NewNop(), unattended: true}
	cli.schedulerBridge = &schedulerBridge{cli: cli}
	adapter := newLiveParkAdapter(cli)
	var _ plugins.ParkCanceller = adapter

	_, err := adapter.CancelPark(park.NewToken(), "x")
	require.Error(t, err, "unknown token must fail before touching the runtime")

	snap := &park.Snapshot{Token: park.NewToken(), Park: park.Request{Mode: park.ModeDelay, Delay: time.Hour}}
	require.NoError(t, snap.Save())
	ch := cli.registerParkWaiter(snap.Token)

	out, err := adapter.CancelPark(snap.Token, "CI already green")
	require.NoError(t, err)
	assert.Contains(t, out, "in-turn waiter")
	select {
	case wake := <-ch:
		assert.Equal(t, parkCancelOutcome, wake.Outcome)
		assert.Equal(t, "CI already green", wake.Detail)
	default:
		t.Fatal("cancel must wake the parked turn immediately")
	}
	// The snapshot stays for the resume to consume — only the resume
	// path retires it.
	_, err = park.Load(snap.Token)
	require.NoError(t, err)

	// A park already queued for resume is not woken twice.
	cli.unregisterParkWaiter(snap.Token)
	cli.pendingResumeQueue = []string{snap.Token}
	_, err = adapter.CancelPark(snap.Token, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already resuming")

	// Without a bridge the runtime is not ready; nothing is deleted.
	cli.pendingResumeQueue = nil
	cli.schedulerBridge = nil
	_, err = adapter.CancelPark(snap.Token, "")
	require.Error(t, err)
	_, err = park.Load(snap.Token)
	require.NoError(t, err)
}
