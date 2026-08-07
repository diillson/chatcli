/*
 * ChatCLI - In-turn park wait tests (unattended surfaces).
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"testing"

	"github.com/diillson/chatcli/cli/agent/park"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func TestWakeParkedAgent_DeliversToInTurnWaiter(t *testing.T) {
	cli := &ChatCLI{logger: zap.NewNop()}
	bridge := &schedulerBridge{cli: cli}

	ch := cli.registerParkWaiter("tok-inline-1")
	err := bridge.wakeParkedAgent("tok-inline-1", "matched", "probe ok")
	assert.NoError(t, err)

	select {
	case wake := <-ch:
		assert.Equal(t, "matched", wake.Outcome)
		assert.Equal(t, "probe ok", wake.Detail)
	default:
		t.Fatal("wake was not delivered to the in-turn waiter")
	}
	// The waiter path must bypass the REPL machinery entirely.
	assert.Empty(t, cli.pendingResumeQueue, "waiter delivery must not also queue")
	assert.Empty(t, cli.parkOutcomes, "waiter delivery must not stash an outcome")
}

func TestWakeParkedAgent_DuplicateWakeDoesNotBlock(t *testing.T) {
	cli := &ChatCLI{logger: zap.NewNop()}
	bridge := &schedulerBridge{cli: cli}

	cli.registerParkWaiter("tok-inline-2")
	// Two wakes for the same token (poll match + safety net): the buffered(1)
	// channel absorbs the first, the second is dropped — never a deadlock.
	assert.NoError(t, bridge.wakeParkedAgent("tok-inline-2", "matched", ""))
	assert.NoError(t, bridge.wakeParkedAgent("tok-inline-2", "error", "job died"))
}

func TestWakeParkedAgent_UnattendedWithoutWaiterQueuesQuietly(t *testing.T) {
	cli := &ChatCLI{logger: zap.NewNop(), unattended: true}
	bridge := &schedulerBridge{cli: cli}

	// No waiter registered (e.g. wake fired after the turn was torn down):
	// the token must still land in the queue for forensics, without banner
	// or TTY inject touching the protocol channel (would panic/corrupt).
	assert.NoError(t, bridge.wakeParkedAgent("tok-inline-3", "elapsed", ""))
	assert.Equal(t, []string{"tok-inline-3"}, cli.pendingResumeQueue)
}

func TestRunParkedInline_CtxCancelRetiresPark(t *testing.T) {
	t.Setenv("CHATCLI_PARK_DIR", t.TempDir())
	cli := &ChatCLI{logger: zap.NewNop(), unattended: true}
	a := &AgentMode{cli: cli, logger: zap.NewNop()}

	snap := &park.Snapshot{Token: park.NewToken()}
	assert.NoError(t, snap.Save())
	cli.registerParkWaiter(snap.Token)
	a.inlineParkToken = snap.Token

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // IDE Stop before any wake arrives

	err := a.runParkedInline(ctx)
	assert.ErrorIs(t, err, context.Canceled)

	// The park must be fully retired: snapshot gone, waiter gone.
	_, loadErr := park.Load(snap.Token)
	assert.Error(t, loadErr, "snapshot must be deleted on turn cancellation")
	assert.Nil(t, cli.parkWaiter(snap.Token), "waiter must be unregistered")
	assert.Empty(t, a.inlineParkToken)
}

func TestRunParkedInline_WakeWithoutSnapshotEndsCleanly(t *testing.T) {
	t.Setenv("CHATCLI_PARK_DIR", t.TempDir())
	cli := &ChatCLI{logger: zap.NewNop(), unattended: true}
	a := &AgentMode{cli: cli, logger: zap.NewNop()}

	ch := cli.registerParkWaiter("tok-inline-4")
	a.inlineParkToken = "tok-inline-4"
	ch <- parkOutcome{Outcome: "elapsed"}

	// The wake races an explicit cancel that already deleted the snapshot:
	// the turn ends cleanly instead of erroring.
	err := a.runParkedInline(context.Background())
	assert.NoError(t, err)
	assert.Empty(t, a.inlineParkToken)
}

func TestRunParkedInline_NoTokenIsNoOp(t *testing.T) {
	cli := &ChatCLI{logger: zap.NewNop()}
	a := &AgentMode{cli: cli, logger: zap.NewNop()}
	assert.NoError(t, a.runParkedInline(context.Background()))
}
