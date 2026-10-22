/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * park_hub_bridge — mirrors the parks this process owns through the
 * Conversation Hub's SQLite store so @park cancel works across processes.
 *
 * A park snapshot lives on disk and any chatcli process on the host can
 * load it, but only the process that parked the agent holds its scheduler
 * job and its wake path (the REPL resume queue or the in-turn waiter of an
 * unattended surface). Before this bridge a cancel issued from another
 * process resumed the agent THERE while the owner's job stayed armed: the
 * same park then resumed twice.
 *
 * Writer side: every sync tick upserts one row per owned park (tokens
 * registered as active parks or holding an in-turn waiter), so heartbeat_at
 * doubles as the owner's liveness signal; rows of parks that stopped being
 * owned are deleted.
 *
 * Reader side: the bridge publishes a process-wide provider that resolves
 * the live owner of a token (another instance with a fresh heartbeat) and
 * writes the wake request onto its row. The owner observes the request on
 * its next tick, cancels its scheduler job and wakes the agent with that
 * outcome — the one wake path that reaches the parked turn.
 */
package cli

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/diillson/chatcli/cli/agent/park"
	"github.com/diillson/chatcli/cli/agent/runs"
	"github.com/diillson/chatcli/cli/scheduler"
	"github.com/diillson/chatcli/server/hub"
	"go.uber.org/zap"
)

const (
	// parkHubStaleAfter is how long an owner row may go without a heartbeat
	// before the owner counts as gone (its process died or shut down).
	parkHubStaleAfter = 15 * time.Second
	// parkHubGCInterval is how often the writer sweeps rows whose owner
	// heartbeat stopped long ago.
	parkHubGCInterval = 10 * time.Minute
	// parkHubOwnerRetention removes owner rows whose heartbeat stopped: a
	// park whose process died is resumed by whoever cancels it next.
	parkHubOwnerRetention = 5 * time.Minute
)

// parkBridgeActive guards against double-starting the bridge in one process.
var parkBridgeActive atomic.Bool

// remoteParkOwner is the live owner of a park mirrored by another process.
type remoteParkOwner struct {
	Instance string
	Origin   string
}

// remoteParkProvider resolves the live owner of a park and forwards wake
// requests to it. Set by the bridge; nil when the hub (or the park mirror)
// is disabled.
type remoteParkProvider struct {
	owner       func(token string) (remoteParkOwner, bool)
	requestWake func(token, outcome, detail string) bool
}

var (
	remoteParkMu     sync.RWMutex
	remoteParkSource *remoteParkProvider
)

// setRemoteParkProvider installs (or clears, with nil) the process-wide
// remote park source.
func setRemoteParkProvider(p *remoteParkProvider) {
	remoteParkMu.Lock()
	remoteParkSource = p
	remoteParkMu.Unlock()
}

// remoteParkLiveOwner returns the other process that owns token and still
// heartbeats. The second result is false when no provider is active, no
// process mirrors the token, or its heartbeat went silent.
func remoteParkLiveOwner(token string) (remoteParkOwner, bool) {
	remoteParkMu.RLock()
	p := remoteParkSource
	remoteParkMu.RUnlock()
	if p == nil || p.owner == nil {
		return remoteParkOwner{}, false
	}
	return p.owner(token)
}

// requestRemoteParkWake asks the owning process to wake token with the
// given outcome. Returns false when no provider is active or no process
// mirrors the token.
func requestRemoteParkWake(token, outcome, detail string) bool {
	remoteParkMu.RLock()
	p := remoteParkSource
	remoteParkMu.RUnlock()
	if p == nil || p.requestWake == nil {
		return false
	}
	return p.requestWake(token, outcome, detail)
}

// ownedParkTokens lists the parks this process can still wake: those
// registered as waiting by the REPL and those holding an in-turn waiter,
// minus any whose resume is already queued (no longer waiting).
func (cli *ChatCLI) ownedParkTokens() []string {
	seen := map[string]bool{}
	var out []string
	cli.activeParkMu.Lock()
	for _, p := range cli.activeParks {
		if !seen[p.Token] {
			seen[p.Token] = true
			out = append(out, p.Token)
		}
	}
	cli.activeParkMu.Unlock()
	cli.parkWaiterMu.Lock()
	for token := range cli.parkWaiters {
		if !seen[token] {
			seen[token] = true
			out = append(out, token)
		}
	}
	cli.parkWaiterMu.Unlock()
	waiting := out[:0]
	for _, token := range out {
		if !cli.hasPendingResume(token) {
			waiting = append(waiting, token)
		}
	}
	return waiting
}

// ownsPark reports whether this process holds token's wake path: it
// registered the park, holds its in-turn waiter, or already queued its
// resume.
func (cli *ChatCLI) ownsPark(token string) bool {
	if cli.parkWaiter(token) != nil || cli.hasPendingResume(token) {
		return true
	}
	cli.activeParkMu.Lock()
	defer cli.activeParkMu.Unlock()
	for _, p := range cli.activeParks {
		if p.Token == token {
			return true
		}
	}
	return false
}

// cancelOwnParkJob drops the scheduler job of a park this process owns
// (best-effort: a job already finished or unknown is reported, not an
// error). Returns a note for the caller.
func (cli *ChatCLI) cancelOwnParkJob(snap *park.Snapshot, reason string) (string, error) {
	if snap == nil || snap.SchedulerJobID == "" || cli.scheduler == nil {
		return "", nil
	}
	owner := scheduler.Owner{Kind: "park", ID: "agent", Tag: snap.Token}
	switch err := cli.scheduler.Cancel(scheduler.JobID(snap.SchedulerJobID), reason, owner); {
	case err == nil:
		return "scheduler job " + snap.SchedulerJobID + " cancelled", nil
	case errors.Is(err, scheduler.ErrJobTerminal), errors.Is(err, scheduler.ErrJobNotFound):
		return "scheduler job " + snap.SchedulerJobID + " was already finished", nil
	default:
		return "", err
	}
}

// honorRemoteParkWake applies a wake another process requested for a park
// this process owns: the local scheduler job is dropped and the agent is
// woken with the requested outcome through the same path the scheduler
// uses.
func (cli *ChatCLI) honorRemoteParkWake(token, outcome, detail string) error {
	if cli.schedulerBridge == nil {
		return errors.New("park runtime not ready (scheduler bridge missing)")
	}
	if cli.hasPendingResume(token) || cli.wasRecentlyResumed(token) {
		return nil // already resuming: a second wake would queue a duplicate resume
	}
	if snap, err := park.Load(token); err == nil {
		if _, cerr := cli.cancelOwnParkJob(snap, detail); cerr != nil {
			return cerr
		}
	}
	return cli.schedulerBridge.wakeParkedAgent(token, outcome, detail)
}

// startParkHubBridge mirrors this process's parks into the hub store and
// honors wake requests other processes leave for them. origin labels the
// surface ("repl", "gateway"). Returns a stop func (never nil). No-ops when
// the store lacks the ParkMirrorStore capability or a bridge is already
// active in this process.
func startParkHubBridge(ctx context.Context, store hub.Store, cli *ChatCLI, origin string, logger *zap.Logger) func() {
	if store == nil || cli == nil {
		return func() {}
	}
	parkStore, ok := store.(hub.ParkMirrorStore)
	if !ok {
		return func() {}
	}
	if !parkBridgeActive.CompareAndSwap(false, true) {
		return func() {}
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	instance := runs.Default().Instance()
	bridgeCtx, cancel := context.WithCancel(ctx)

	setRemoteParkProvider(&remoteParkProvider{
		owner: func(token string) (remoteParkOwner, bool) {
			opCtx, opCancel := context.WithTimeout(bridgeCtx, runsHubOpTimeout)
			defer opCancel()
			recs, err := parkStore.ListParkMirror(opCtx)
			if err != nil {
				logger.Debug("park hub: list failed", zap.Error(err))
				return remoteParkOwner{}, false
			}
			for _, rec := range recs {
				if rec.Token != token || rec.Instance == instance {
					continue
				}
				if time.Since(rec.HeartbeatAt) > parkHubStaleAfter {
					return remoteParkOwner{}, false
				}
				return remoteParkOwner{Instance: rec.Instance, Origin: rec.Origin}, true
			}
			return remoteParkOwner{}, false
		},
		requestWake: func(token, outcome, detail string) bool {
			opCtx, opCancel := context.WithTimeout(bridgeCtx, runsHubOpTimeout)
			defer opCancel()
			ok, err := parkStore.RequestParkWake(opCtx, token, outcome, detail)
			if err != nil {
				logger.Warn("park hub: wake request failed", zap.String("token", token), zap.Error(err))
				return false
			}
			return ok
		},
	})

	// mirrored is the set of tokens heartbeated on the previous tick, so a
	// park that stopped being owned has its row removed.
	mirrored := map[string]bool{}

	flush := func() {
		owned := cli.ownedParkTokens()
		current := make(map[string]bool, len(owned))
		for _, token := range owned {
			current[token] = true
			opCtx, opCancel := context.WithTimeout(bridgeCtx, runsHubOpTimeout)
			err := parkStore.UpsertParkOwner(opCtx, hub.ParkMirrorRecord{Token: token, Instance: instance, Origin: origin})
			opCancel()
			if err != nil {
				logger.Debug("park hub: heartbeat failed", zap.String("token", token), zap.Error(err))
			}
		}
		for token := range mirrored {
			if current[token] {
				continue
			}
			opCtx, opCancel := context.WithTimeout(bridgeCtx, runsHubOpTimeout)
			_ = parkStore.DeleteParkMirror(opCtx, token)
			opCancel()
		}
		mirrored = current

		// Honor wake requests targeting parks owned by this process.
		opCtx, opCancel := context.WithTimeout(bridgeCtx, runsHubOpTimeout)
		recs, err := parkStore.ListParkMirror(opCtx)
		opCancel()
		if err != nil {
			return
		}
		for _, rec := range recs {
			if rec.Instance != instance || rec.WakeOutcome == "" {
				continue
			}
			if err := cli.honorRemoteParkWake(rec.Token, rec.WakeOutcome, rec.WakeDetail); err != nil {
				logger.Warn("park hub: cross-process wake failed", zap.String("token", rec.Token), zap.Error(err))
				continue
			}
			logger.Info("park hub: cross-process wake honored",
				zap.String("token", rec.Token), zap.String("outcome", rec.WakeOutcome))
			opCtx, opCancel := context.WithTimeout(bridgeCtx, runsHubOpTimeout)
			_ = parkStore.DeleteParkMirror(opCtx, rec.Token)
			opCancel()
			delete(mirrored, rec.Token)
		}
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(squadMailPollInterval())
		defer ticker.Stop()
		gcTicker := time.NewTicker(parkHubGCInterval)
		defer gcTicker.Stop()

		gc := func() {
			opCtx, opCancel := context.WithTimeout(bridgeCtx, runsHubOpTimeout)
			defer opCancel()
			if n, err := parkStore.PurgeParkMirror(opCtx, parkHubOwnerRetention); err == nil && n > 0 {
				logger.Debug("park hub: purged stale park owners", zap.Int64("count", n))
			}
		}
		gc()
		for {
			select {
			case <-bridgeCtx.Done():
				// Release our rows: other processes may now resume these
				// parks themselves instead of waiting on a dead owner.
				for token := range mirrored {
					opCtx, opCancel := context.WithTimeout(context.WithoutCancel(bridgeCtx), runsHubOpTimeout)
					_ = parkStore.DeleteParkMirror(opCtx, token)
					opCancel()
				}
				return
			case <-ticker.C:
				flush()
			case <-gcTicker.C:
				gc()
			}
		}
	}()

	return func() {
		cancel()
		<-done
		setRemoteParkProvider(nil)
		parkBridgeActive.Store(false)
	}
}

// describeParkOwner names a remote owner for the model: its surface and
// instance token.
func describeParkOwner(o remoteParkOwner) string {
	parts := []string{}
	if o.Origin != "" {
		parts = append(parts, o.Origin)
	}
	if o.Instance != "" {
		parts = append(parts, "instance "+o.Instance)
	}
	if len(parts) == 0 {
		return "another process"
	}
	return strings.Join(parts, ", ")
}
