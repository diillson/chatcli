/*
 * ChatCLI - In-turn park wait for unattended surfaces.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * On the REPL, a parked agent ends its run and the outer prompt loop later
 * drains pendingResumeQueue to re-enter the loop. ACP (IDE), the MCP server
 * and the gateway have no such loop: a park taken there used to end the turn
 * with nothing ever consuming the resume — the monitor kept firing forever,
 * invisible, and the client showed an eternally pending turn.
 *
 * This file keeps the park IN-TURN on those surfaces: handleAgentPark
 * registers a waiter channel before enqueueing the scheduler job, Run()
 * blocks in runParkedInline instead of returning, the bridge delivers the
 * wake straight to the waiter, and the resumed loop continues on the same
 * request with the same event sink. Cancelling the turn (IDE Stop /
 * session/cancel) cancels the scheduler job and deletes the snapshot so
 * nothing leaks.
 */
package cli

import (
	"context"
	"errors"

	"github.com/diillson/chatcli/cli/agent/park"
	"github.com/diillson/chatcli/cli/scheduler"
	"go.uber.org/zap"
)

// registerParkWaiter installs the in-turn wake channel for token. Buffered(1)
// so the scheduler dispatcher's send never blocks.
func (cli *ChatCLI) registerParkWaiter(token string) chan parkOutcome {
	ch := make(chan parkOutcome, 1)
	cli.parkWaiterMu.Lock()
	if cli.parkWaiters == nil {
		cli.parkWaiters = map[string]chan parkOutcome{}
	}
	cli.parkWaiters[token] = ch
	cli.parkWaiterMu.Unlock()
	return ch
}

// parkWaiter returns the registered wake channel for token, or nil.
func (cli *ChatCLI) parkWaiter(token string) chan parkOutcome {
	cli.parkWaiterMu.Lock()
	defer cli.parkWaiterMu.Unlock()
	return cli.parkWaiters[token]
}

// unregisterParkWaiter removes the wake channel for token.
func (cli *ChatCLI) unregisterParkWaiter(token string) {
	cli.parkWaiterMu.Lock()
	delete(cli.parkWaiters, token)
	cli.parkWaiterMu.Unlock()
}

// runParkedInline blocks the current run on the registered park waiter and
// re-enters the loop when the scheduler delivers the wake. Re-parks inside
// the resumed loop (the normal monitoring cycle) land back here, so one
// client request covers the whole monitoring conversation. Returns when the
// resumed loop completes without re-parking, fails, or ctx is cancelled.
func (a *AgentMode) runParkedInline(ctx context.Context) error {
	for {
		token := a.inlineParkToken
		if token == "" {
			return nil
		}
		ch := a.cli.parkWaiter(token)
		if ch == nil {
			// Defensive: no waiter means the park was cancelled elsewhere.
			a.inlineParkToken = ""
			return nil
		}

		var wake parkOutcome
		select {
		case wake = <-ch:
		case <-ctx.Done():
			// IDE Stop / session cancel / client disconnect: retire the park
			// completely — job, snapshot, waiter — so nothing keeps polling
			// for a turn that no longer exists.
			a.cancelInlinePark(token)
			return ctx.Err()
		}

		a.cli.unregisterParkWaiter(token)
		a.inlineParkToken = ""

		snap, err := park.Load(token)
		if err != nil {
			// Snapshot gone (raced with an explicit cancel): clean end.
			a.logger.Info("park: inline wake without snapshot; ending turn",
				zap.String("token", token), zap.Error(err))
			return nil
		}

		a.logger.Info("park: inline wake received; resuming in-turn",
			zap.String("token", token),
			zap.String("outcome", wake.Outcome))

		err = a.runResumedCore(ctx, snap, wake.Outcome, wake.Detail)
		if errors.Is(err, errAgentParkedRequested) {
			// The monitoring cycle re-parked: handleAgentPark already
			// registered the next waiter and set inlineParkToken.
			continue
		}
		return err
	}
}

// cancelInlinePark retires a park whose in-turn waiter was abandoned:
// scheduler job cancelled, snapshot deleted, bridge bookkeeping cleared.
// Mirrors /cancel-park, which is unreachable from unattended surfaces.
func (a *AgentMode) cancelInlinePark(token string) {
	if snap, err := park.Load(token); err == nil && snap.SchedulerJobID != "" && a.cli.scheduler != nil {
		owner := scheduler.Owner{Kind: "park", ID: "agent", Tag: snap.Token}
		if cerr := a.cli.scheduler.Cancel(scheduler.JobID(snap.SchedulerJobID), "unattended turn cancelled", owner); cerr != nil {
			a.logger.Warn("park: cancel job on turn abort failed",
				zap.String("token", token), zap.Error(cerr))
		}
	}
	if err := park.Delete(token); err != nil {
		a.logger.Warn("park: delete snapshot on turn abort failed",
			zap.String("token", token), zap.Error(err))
	}
	a.cli.dropPendingResume(token)
	a.cli.unregisterActivePark(token)
	a.cli.unregisterParkWaiter(token)
	a.cli.parkOutcomeMu.Lock()
	delete(a.cli.parkOutcomes, token)
	a.cli.parkOutcomeMu.Unlock()
	a.inlineParkToken = ""
	a.logger.Info("park: retired on turn cancellation", zap.String("token", token))
}
